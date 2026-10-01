package scan

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/internal/scan/obligation"
	"github.com/lycaon/lycaon/internal/scan/rules"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
)

type TriggerCoordinator interface {
	PublishPending(ctx context.Context, scanID string) error
}

type TriggerService struct {
	Coordinator TriggerCoordinator
	Registry    CodeScannerRegistry
	Gates       scancfg.GatesConfig
	Settings    *settings.SecurityScannersStore
}

func (s *TriggerService) securityOn() bool {
	if s == nil || s.Settings == nil {
		return true
	}
	return s.Settings.Effective().Enabled
}

// PrepareOverlayPromotion resolves scan policy before the merge commit.
func (s *TriggerService) PrepareOverlayPromotion(ctx context.Context, task api.WorkerTask, appliedPaths, deletedPaths []string) (obligation.Plan, error) {
	canonical, err := CanonicalPath(task.WorkspacePath)
	if err != nil {
		return obligation.Plan{}, err
	}
	changed, err := NormalizeLandedPaths(canonical, appliedPaths)
	if err != nil {
		return obligation.Plan{}, err
	}
	deleted, err := NormalizeLandedPaths(canonical, deletedPaths)
	if err != nil {
		return obligation.Plan{}, err
	}
	deleted = intersectPaths(changed, deleted)
	now := time.Now().UTC()
	plan := obligation.Plan{
		ID:            uuid.NewString(),
		WorkerJobID:   task.ID,
		CanonicalPath: canonical,
		DelegationID:  task.DelegationID,
		WorkflowRunID: task.WorkflowRunID,
		ChangedPaths:  changed,
		DeletedPaths:  deleted,
		CreatedAt:     now,
	}
	if s == nil || !s.securityOn() || !s.Gates.Gates.LandedChange.Enabled || len(changed) == 0 {
		return plan, nil
	}
	plan.Required = true
	plan.ScanID = uuid.NewString()
	plan.AssessmentID = uuid.NewString()
	if s.Registry == nil {
		plan.InitialFailure = "static-analysis scanner registry is unavailable"
		plan.InitialFailureCode = "SCAN_REGISTRY_UNAVAILABLE"
		return plan, nil
	}
	candidates := ListSelectedScanners(ctx, s.Registry, canonical, api.ScanCategorySAST)
	switch len(candidates) {
	case 0:
		plan.InitialFailure = "no selected static-analysis scanner is runnable"
		plan.InitialFailureCode = "SCAN_SCANNER_UNAVAILABLE"
	case 1:
		plan.ScannerID = candidates[0].ID
	default:
		plan.InitialFailure = fmt.Sprintf("static-analysis slot resolved to %d scanners", len(candidates))
		plan.InitialFailureCode = "SCAN_SCANNER_AMBIGUOUS"
	}
	if plan.InitialFailure == "" {
		contract, contractErr := SelectedScannerContract(ctx, s.Registry, canonical, plan.ScannerID, api.ScanCategorySAST)
		if contractErr != nil {
			plan.InitialFailure = contractErr.Error()
			plan.InitialFailureCode = "SCAN_DEFINITION_UNAVAILABLE"
		} else {
			manifest, fingerprint, manifestErr := scancatalog.ExecutionManifest(contract)
			if manifestErr != nil {
				plan.InitialFailure = manifestErr.Error()
				plan.InitialFailureCode = "SCAN_DEFINITION_UNAVAILABLE"
			} else {
				plan.ExecutionManifest = manifest
				plan.ExecutionFingerprint = fingerprint
				plan.FingerprintScheme = api.ScanFingerprintScheme
			}
		}
	}
	switch scancfg.EffectiveLandedChangeScope(s.Gates, s.Settings) {
	case scancfg.LandedChangeScopeFullRoot:
	case scancfg.LandedChangeScopePathScoped:
		plan.PathScoped = true
		if len(obligation.PathScopedTarget(canonical, changed)) == 0 && plan.InitialFailure == "" {
			plan.InitialFailure = "changed paths have no scannable target"
			plan.InitialFailureCode = "SCAN_TARGET_UNAVAILABLE"
		}
	default:
		plan.InitialFailure = "unknown landed-change scan scope"
		plan.InitialFailureCode = "SCAN_SCOPE_INVALID"
	}
	return plan, nil
}

func (s *TriggerService) PublishObligation(ctx context.Context, plan obligation.Plan) error {
	if !plan.Required || plan.InitialFailure != "" {
		return nil
	}
	if s == nil || s.Coordinator == nil {
		return fmt.Errorf("scan coordinator cannot publish obligations")
	}
	return s.Coordinator.PublishPending(ctx, plan.ScanID)
}

func NormalizeLandedPaths(root string, paths []string) ([]string, error) {
	excludes, err := rules.LoadPathExcludes()
	if err != nil {
		return nil, fmt.Errorf("load scan path exclusions: %w", err)
	}
	patterns := excludes.Patterns()
	out := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		rel, ok := BoundRelUnderRoot(root, path)
		if !ok {
			continue
		}
		if rules.PathUnderExclude(rel, patterns) {
			continue
		}
		if _, exists := seen[rel]; exists {
			continue
		}
		seen[rel] = struct{}{}
		out = append(out, rel)
	}
	return out, nil
}

func intersectPaths(paths, selected []string) []string {
	wanted := make(map[string]struct{}, len(selected))
	for _, path := range selected {
		wanted[path] = struct{}{}
	}
	out := make([]string, 0, len(selected))
	for _, path := range paths {
		if _, ok := wanted[path]; ok {
			out = append(out, path)
		}
	}
	return out
}
