package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/workspace"
)

// PromotionEngine rolls durable promotion intents forward to completion.
type PromotionEngine struct {
	registry Registry
	initGit  func(context.Context, string) error
}

func NewPromotionEngine(registry Registry, initGit func(context.Context, string) error) *PromotionEngine {
	return &PromotionEngine{registry: registry, initGit: initGit}
}

// Create records a new intent before performing any filesystem mutation.
func (e *PromotionEngine) Create(ctx context.Context, projectID, destination string, initGit bool) (*Promotion, error) {
	if e == nil || e.registry == nil {
		return nil, fmt.Errorf("project registry not configured")
	}
	return e.registry.CreatePromotion(ctx, projectID, destination, initGit)
}

// Run resumes one promotion from its durable phase and returns only after cleanup.
func (e *PromotionEngine) Run(ctx context.Context, projectID string) (*Project, error) {
	for {
		promotion, err := e.registry.GetPromotion(ctx, projectID)
		if err != nil {
			return nil, err
		}
		if err := validatePromotionPaths(promotion); err != nil {
			return nil, err
		}
		var p *Project
		switch promotion.Phase {
		case PromotionQueued:
			err = e.stage(ctx, promotion)
		case PromotionStaged:
			err = e.install(ctx, promotion)
		case PromotionInstalled:
			p, err = e.commit(ctx, promotion)
		case PromotionCommitted:
			p, err = e.cleanup(ctx, promotion)
		default:
			err = fmt.Errorf("%w: unknown phase %q", ErrPromotionPhase, promotion.Phase)
		}
		if err != nil {
			_, _ = e.registry.AdvancePromotion(
				context.WithoutCancel(ctx), projectID, promotion.Phase, promotion.Phase,
				promotion.SourceSHA256, promotion.ManifestSHA256, err.Error(),
			)
			return nil, err
		}
		if p != nil && p.Promotion == nil {
			return p, nil
		}
	}
}

// Cancel removes an uncommitted intent and restores the chosen empty folder.
func (e *PromotionEngine) Cancel(ctx context.Context, projectID string) (*Project, error) {
	promotion, err := e.registry.GetPromotion(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if err := validatePromotionPaths(promotion); err != nil {
		return nil, err
	}
	switch promotion.Phase {
	case PromotionQueued:
		if err := restoreDestinationAfterInterruptedStage(promotion); err != nil {
			return nil, err
		}
		if err := os.RemoveAll(promotion.StagePath); err != nil {
			return nil, err
		}
	case PromotionStaged:
		stageExists, inspectErr := promotionPathExists(promotion.StagePath)
		if inspectErr != nil {
			return nil, inspectErr
		}
		if stageExists {
			if err := os.RemoveAll(promotion.StagePath); err != nil {
				return nil, err
			}
			if err := restoreDestinationAfterInterruptedStage(promotion); err != nil {
				return nil, err
			}
		} else if err := rollbackInstalledPromotion(ctx, promotion); err != nil {
			return nil, err
		}
	case PromotionInstalled:
		if err := rollbackInstalledPromotion(ctx, promotion); err != nil {
			return nil, err
		}
	case PromotionCommitted:
		return nil, fmt.Errorf("%w: committed promotion cannot be canceled", ErrPromotionPhase)
	default:
		return nil, fmt.Errorf("%w: unknown phase %q", ErrPromotionPhase, promotion.Phase)
	}
	return e.registry.CancelPromotion(ctx, projectID)
}

func (e *PromotionEngine) stage(ctx context.Context, promotion *Promotion) error {
	if err := restoreDestinationAfterInterruptedStage(promotion); err != nil {
		return err
	}
	if err := os.RemoveAll(promotion.StagePath); err != nil {
		return err
	}
	if err := workspace.CopyTreeExact(ctx, promotion.SourcePath, promotion.StagePath); err != nil {
		return fmt.Errorf("stage draft workspace: %w", err)
	}
	sourceDigest, err := workspace.TreeSHA256(ctx, promotion.SourcePath)
	if err != nil {
		return fmt.Errorf("hash draft workspace: %w", err)
	}
	stagedSourceDigest, err := workspace.TreeSHA256(ctx, promotion.StagePath)
	if err != nil {
		return fmt.Errorf("hash staged workspace: %w", err)
	}
	if stagedSourceDigest != sourceDigest {
		return fmt.Errorf("draft workspace changed while it was staged")
	}
	if promotion.InitGit {
		if e.initGit == nil {
			return fmt.Errorf("git is not configured")
		}
		if err := e.initGit(ctx, promotion.StagePath); err != nil {
			return fmt.Errorf("initialize staged repository: %w", err)
		}
	}
	digest, err := workspace.TreeSHA256(ctx, promotion.StagePath)
	if err != nil {
		return fmt.Errorf("hash staged workspace: %w", err)
	}
	_, err = e.registry.AdvancePromotion(
		ctx, promotion.ProjectID, PromotionQueued, PromotionStaged, sourceDigest, digest, "",
	)
	return err
}

func (e *PromotionEngine) install(ctx context.Context, promotion *Promotion) error {
	stageExists, err := promotionPathExists(promotion.StagePath)
	if err != nil {
		return err
	}
	reservationExists, err := promotionPathExists(promotion.ReservationPath)
	if err != nil {
		return err
	}
	destinationExists, err := promotionPathExists(promotion.DestinationPath)
	if err != nil {
		return err
	}
	switch {
	case stageExists && !reservationExists && destinationExists:
		digest, err := workspace.TreeSHA256(ctx, promotion.StagePath)
		if err != nil {
			return err
		}
		if digest != promotion.ManifestSHA256 {
			return fmt.Errorf("staged workspace changed before install")
		}
		if _, err := resolvePromotionDestination(promotion.DestinationPath); err != nil {
			return err
		}
		if err := os.Rename(promotion.DestinationPath, promotion.ReservationPath); err != nil {
			return fmt.Errorf("reserve destination: %w", err)
		}
		if err := os.Rename(promotion.StagePath, promotion.DestinationPath); err != nil {
			_ = os.Rename(promotion.ReservationPath, promotion.DestinationPath)
			return fmt.Errorf("install staged workspace: %w", err)
		}
	case stageExists && reservationExists && !destinationExists:
		digest, err := workspace.TreeSHA256(ctx, promotion.StagePath)
		if err != nil {
			return err
		}
		if digest != promotion.ManifestSHA256 {
			return fmt.Errorf("staged workspace changed before install")
		}
		if err := os.Rename(promotion.StagePath, promotion.DestinationPath); err != nil {
			return fmt.Errorf("finish staged workspace install: %w", err)
		}
	case !stageExists && reservationExists && destinationExists:
		// The two renames completed before the durable phase advanced.
	case stageExists && reservationExists && destinationExists:
		return fmt.Errorf("promotion stage, reservation, and destination all exist")
	default:
		return fmt.Errorf("staged workspace is missing")
	}
	digest, err := workspace.TreeSHA256(ctx, promotion.DestinationPath)
	if err != nil {
		return err
	}
	if digest != promotion.ManifestSHA256 {
		return fmt.Errorf("installed workspace does not match staged manifest")
	}
	_, err = e.registry.AdvancePromotion(
		ctx, promotion.ProjectID, PromotionStaged, PromotionInstalled,
		promotion.SourceSHA256, digest, "",
	)
	return err
}

func (e *PromotionEngine) commit(ctx context.Context, promotion *Promotion) (*Project, error) {
	if err := verifyPromotionSource(ctx, promotion); err != nil {
		return nil, err
	}
	digest, err := workspace.TreeSHA256(ctx, promotion.DestinationPath)
	if err != nil {
		return nil, err
	}
	if digest != promotion.ManifestSHA256 {
		return nil, fmt.Errorf("installed workspace changed before commit")
	}
	return e.registry.CommitPromotion(ctx, promotion.ProjectID)
}

func (e *PromotionEngine) cleanup(ctx context.Context, promotion *Promotion) (*Project, error) {
	p, err := e.registry.Get(ctx, promotion.ProjectID)
	if err != nil {
		return nil, err
	}
	root, ok := rootByID(p.Roots, promotion.RootID)
	if !ok || root.Kind != RootKindAttached || !SamePath(root.Path, promotion.DestinationPath) {
		return nil, fmt.Errorf("committed promotion root does not match destination")
	}
	sourceExists, err := promotionPathExists(promotion.SourcePath)
	if err != nil {
		return nil, err
	}
	if sourceExists {
		if err := verifyPromotionSource(ctx, promotion); err != nil {
			return nil, err
		}
	}
	if err := RemoveDraftScratch(filepath.Dir(filepath.Dir(promotion.SourcePath)), promotion.ProjectID); err != nil {
		return nil, fmt.Errorf("remove draft workspace: %w", err)
	}
	if err := removeReservation(promotion.ReservationPath); err != nil {
		return nil, err
	}
	if err := e.registry.DeletePromotion(ctx, promotion.ProjectID); err != nil {
		return nil, err
	}
	p.Promotion = nil
	return p, nil
}

func verifyPromotionSource(ctx context.Context, promotion *Promotion) error {
	digest, err := workspace.TreeSHA256(ctx, promotion.SourcePath)
	if err != nil {
		return fmt.Errorf("hash draft workspace: %w", err)
	}
	if digest != promotion.SourceSHA256 {
		return fmt.Errorf("draft workspace changed after staging; source was preserved")
	}
	return nil
}

func promotionPaths(projectID, destination string) (string, string) {
	id := strings.TrimSpace(projectID)
	return destination + ".paintedwolf-promote-" + id + ".stage",
		destination + ".paintedwolf-promote-" + id + ".reservation"
}

func validatePromotionPaths(promotion *Promotion) error {
	if promotion == nil || strings.TrimSpace(promotion.ProjectID) == "" {
		return fmt.Errorf("promotion identity is required")
	}
	wantStage, wantReservation := promotionPaths(promotion.ProjectID, promotion.DestinationPath)
	if filepath.Clean(promotion.StagePath) != filepath.Clean(wantStage) ||
		filepath.Clean(promotion.ReservationPath) != filepath.Clean(wantReservation) {
		return fmt.Errorf("promotion work paths do not match the destination")
	}
	return nil
}

func restoreDestinationAfterInterruptedStage(promotion *Promotion) error {
	reservationExists, err := promotionPathExists(promotion.ReservationPath)
	if err != nil {
		return err
	}
	if !reservationExists {
		return nil
	}
	destinationExists, err := promotionPathExists(promotion.DestinationPath)
	if err != nil {
		return err
	}
	if destinationExists {
		return fmt.Errorf("both promotion destination and reservation exist")
	}
	if err := os.Rename(promotion.ReservationPath, promotion.DestinationPath); err != nil {
		return fmt.Errorf("restore promotion destination: %w", err)
	}
	return nil
}

func rollbackInstalledPromotion(ctx context.Context, promotion *Promotion) error {
	reservationExists, err := promotionPathExists(promotion.ReservationPath)
	if err != nil {
		return err
	}
	destinationExists, err := promotionPathExists(promotion.DestinationPath)
	if err != nil {
		return err
	}
	if !reservationExists || !destinationExists {
		return fmt.Errorf("installed promotion cannot restore its destination")
	}
	digest, err := workspace.TreeSHA256(ctx, promotion.DestinationPath)
	if err != nil {
		return err
	}
	if digest != promotion.ManifestSHA256 {
		return fmt.Errorf("installed workspace changed; it was preserved")
	}
	stageExists, err := promotionPathExists(promotion.StagePath)
	if err != nil {
		return err
	}
	if stageExists {
		return fmt.Errorf("promotion stage already exists")
	}
	if err := os.Rename(promotion.DestinationPath, promotion.StagePath); err != nil {
		return fmt.Errorf("uninstall promoted workspace: %w", err)
	}
	if err := os.Rename(promotion.ReservationPath, promotion.DestinationPath); err != nil {
		_ = os.Rename(promotion.StagePath, promotion.DestinationPath)
		return fmt.Errorf("restore chosen folder: %w", err)
	}
	if err := os.RemoveAll(promotion.StagePath); err != nil {
		return fmt.Errorf("remove canceled stage: %w", err)
	}
	return nil
}

func removeReservation(path string) error {
	entries, err := os.ReadDir(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != ".DS_Store" {
			return fmt.Errorf("%w: promotion reservation", ErrPromotionDestinationNotEmpty)
		}
		if err := os.Remove(filepath.Join(path, entry.Name())); err != nil {
			return err
		}
	}
	return os.Remove(path)
}

func promotionPathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("inspect promotion path %q: %w", path, err)
}
