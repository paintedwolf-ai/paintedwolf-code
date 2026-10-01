package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

var (
	ErrPromotionNotFound = errors.New("project promotion not found")
	ErrPromotionConflict = errors.New("project promotion already exists")
	ErrPromotionPhase    = errors.New("project promotion phase conflict")
)

func (r *MemoryRegistry) CreatePromotion(
	ctx context.Context,
	projectID, destPath string,
	initGit bool,
) (*Promotion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	folder, err := resolvePromotionDestination(destPath)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.projects[projectID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, projectID)
	}
	root, err := DraftPromotionRoot(p)
	if err != nil {
		return nil, err
	}
	if _, exists := r.promotions[projectID]; exists {
		return nil, ErrPromotionConflict
	}
	stagePath, reservationPath := promotionPaths(projectID, folder)
	now := time.Now().UTC()
	promotion := &Promotion{
		ProjectID:       projectID,
		RootID:          root.ID,
		SourcePath:      root.Path,
		DestinationPath: folder,
		StagePath:       stagePath,
		ReservationPath: reservationPath,
		InitGit:         initGit,
		Phase:           PromotionQueued,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	r.promotions[projectID] = promotion
	p.Promotion = clonePromotion(promotion)
	return clonePromotion(promotion), nil
}

func (r *MemoryRegistry) GetPromotion(ctx context.Context, projectID string) (*Promotion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	promotion, ok := r.promotions[projectID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrPromotionNotFound, projectID)
	}
	return clonePromotion(promotion), nil
}

func (r *MemoryRegistry) ListPromotions(ctx context.Context) ([]Promotion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Promotion, 0, len(r.promotions))
	for _, promotion := range r.promotions {
		out = append(out, *clonePromotion(promotion))
	}
	return out, nil
}

func (r *MemoryRegistry) AdvancePromotion(
	ctx context.Context,
	projectID string,
	from, to PromotionPhase,
	sourceSHA256, manifestSHA256, lastError string,
) (*Promotion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	promotion, ok := r.promotions[projectID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrPromotionNotFound, projectID)
	}
	if promotion.Phase != from {
		return nil, fmt.Errorf("%w: have %s want %s", ErrPromotionPhase, promotion.Phase, from)
	}
	promotion.Phase = to
	if strings.TrimSpace(sourceSHA256) != "" {
		promotion.SourceSHA256 = strings.TrimSpace(sourceSHA256)
	}
	if strings.TrimSpace(manifestSHA256) != "" {
		promotion.ManifestSHA256 = strings.TrimSpace(manifestSHA256)
	}
	promotion.LastError = strings.TrimSpace(lastError)
	promotion.UpdatedAt = time.Now().UTC()
	if p := r.projects[projectID]; p != nil {
		p.Promotion = clonePromotion(promotion)
	}
	return clonePromotion(promotion), nil
}

func (r *MemoryRegistry) CommitPromotion(ctx context.Context, projectID string) (*Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.projects[projectID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, projectID)
	}
	promotion, ok := r.promotions[projectID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrPromotionNotFound, projectID)
	}
	if promotion.Phase != PromotionInstalled {
		return nil, fmt.Errorf("%w: have %s want %s", ErrPromotionPhase, promotion.Phase, PromotionInstalled)
	}
	root, err := DraftPromotionRoot(p)
	if err != nil {
		return nil, err
	}
	if root.ID != promotion.RootID || !SamePath(root.Path, promotion.SourcePath) {
		return nil, ErrDraftRootInvariant
	}
	p.Roots[0].Path = promotion.DestinationPath
	p.Roots[0].Label = deriveUniqueRootLabel(nil, promotion.DestinationPath)
	p.Roots[0].Kind = RootKindAttached
	p.IsDraft = false
	if strings.TrimSpace(p.Name) == "" {
		p.Name = NameFromFolderPath(promotion.DestinationPath)
	}
	p.RootsGeneration++
	promotion.Phase = PromotionCommitted
	promotion.LastError = ""
	promotion.UpdatedAt = time.Now().UTC()
	p.Promotion = clonePromotion(promotion)
	return cloneProject(p), nil
}

func (r *MemoryRegistry) DeletePromotion(ctx context.Context, projectID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	promotion, ok := r.promotions[projectID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrPromotionNotFound, projectID)
	}
	if promotion.Phase != PromotionCommitted {
		return fmt.Errorf("%w: have %s want %s", ErrPromotionPhase, promotion.Phase, PromotionCommitted)
	}
	delete(r.promotions, projectID)
	if p := r.projects[projectID]; p != nil {
		p.Promotion = nil
	}
	return nil
}

func (r *MemoryRegistry) CancelPromotion(ctx context.Context, projectID string) (*Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	promotion, ok := r.promotions[projectID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrPromotionNotFound, projectID)
	}
	if promotion.Phase == PromotionCommitted {
		return nil, fmt.Errorf("%w: committed promotion cannot be canceled", ErrPromotionPhase)
	}
	delete(r.promotions, projectID)
	p, ok := r.projects[projectID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, projectID)
	}
	p.Promotion = nil
	return cloneProject(p), nil
}

func clonePromotion(promotion *Promotion) *Promotion {
	if promotion == nil {
		return nil
	}
	out := *promotion
	return &out
}

func (r *SQLRegistry) CreatePromotion(
	ctx context.Context,
	projectID, destPath string,
	initGit bool,
) (*Promotion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	folder, err := resolvePromotionDestination(destPath)
	if err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := r.queries.WithTx(tx)
	p, err := loadProject(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	root, err := DraftPromotionRoot(p)
	if err != nil {
		return nil, err
	}
	stagePath, reservationPath := promotionPaths(projectID, folder)
	now := time.Now().UTC()
	if err := q.CreateProjectPromotion(ctx, db.CreateProjectPromotionParams{
		ProjectID:       projectID,
		RootID:          root.ID,
		SourcePath:      root.Path,
		DestinationPath: folder,
		StagePath:       stagePath,
		ReservationPath: reservationPath,
		InitGit:         boolToInt64(initGit),
		CreatedAt:       db.FormatTime(now),
		UpdatedAt:       db.FormatTime(now),
	}); err != nil {
		if db.IsUniqueConstraint(err) {
			return nil, ErrPromotionConflict
		}
		return nil, err
	}
	row, err := q.GetProjectPromotion(ctx, projectID)
	if err != nil {
		return nil, err
	}
	promotion, err := promotionFromRow(row)
	if err != nil {
		return nil, err
	}
	p.Promotion = promotion
	if err := r.enqueueProjectEventTx(ctx, tx, api.ProjectEventUpdated, p); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	r.notifyProjectEvents()
	return promotion, nil
}

func (r *SQLRegistry) GetPromotion(ctx context.Context, projectID string) (*Promotion, error) {
	row, err := r.queries.GetProjectPromotion(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrPromotionNotFound, projectID)
	}
	if err != nil {
		return nil, err
	}
	return promotionFromRow(row)
}

func (r *SQLRegistry) ListPromotions(ctx context.Context) ([]Promotion, error) {
	rows, err := r.queries.ListProjectPromotions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Promotion, 0, len(rows))
	for _, row := range rows {
		promotion, parseErr := promotionFromRow(row)
		if parseErr != nil {
			return nil, parseErr
		}
		out = append(out, *promotion)
	}
	return out, nil
}

func (r *SQLRegistry) AdvancePromotion(
	ctx context.Context,
	projectID string,
	from, to PromotionPhase,
	sourceSHA256, manifestSHA256, lastError string,
) (*Promotion, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := r.queries.WithTx(tx)
	n, err := q.AdvanceProjectPromotion(ctx, db.AdvanceProjectPromotionParams{
		Phase:          string(to),
		SourceSha256:   strings.TrimSpace(sourceSHA256),
		ManifestSha256: strings.TrimSpace(manifestSHA256),
		LastError:      strings.TrimSpace(lastError),
		UpdatedAt:      db.FormatTime(time.Now().UTC()),
		ProjectID:      projectID,
		Phase_2:        string(from),
	})
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, ErrPromotionPhase
	}
	p, err := loadProject(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	if err := r.enqueueProjectEventTx(ctx, tx, api.ProjectEventUpdated, p); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	r.notifyProjectEvents()
	return clonePromotion(p.Promotion), nil
}

func (r *SQLRegistry) CommitPromotion(ctx context.Context, projectID string) (*Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := r.queries.WithTx(tx)
	promotionRow, err := q.GetProjectPromotion(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrPromotionNotFound, projectID)
	}
	if err != nil {
		return nil, err
	}
	promotion, err := promotionFromRow(promotionRow)
	if err != nil {
		return nil, err
	}
	if promotion.Phase != PromotionInstalled {
		return nil, fmt.Errorf("%w: have %s want %s", ErrPromotionPhase, promotion.Phase, PromotionInstalled)
	}
	p, err := loadProject(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	root, err := DraftPromotionRoot(p)
	if err != nil {
		return nil, err
	}
	if root.ID != promotion.RootID || !SamePath(root.Path, promotion.SourcePath) {
		return nil, ErrDraftRootInvariant
	}
	if err := q.PromoteProjectRoot(ctx, db.PromoteProjectRootParams{
		Path:      promotion.DestinationPath,
		Label:     deriveUniqueRootLabel(nil, promotion.DestinationPath),
		ID:        promotion.RootID,
		ProjectID: projectID,
	}); err != nil {
		return nil, err
	}
	if err := q.BumpProjectRootsGeneration(ctx, projectID); err != nil {
		return nil, err
	}
	if folderName := NameFromFolderPath(promotion.DestinationPath); folderName != "" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE projects SET name = ?
			WHERE id = ? AND (name IS NULL OR TRIM(name) = '')
		`, db.NullString(folderName), projectID); err != nil {
			return nil, err
		}
	}
	n, err := q.AdvanceProjectPromotion(ctx, db.AdvanceProjectPromotionParams{
		Phase:          string(PromotionCommitted),
		SourceSha256:   promotion.SourceSHA256,
		ManifestSha256: promotion.ManifestSHA256,
		UpdatedAt:      db.FormatTime(time.Now().UTC()),
		ProjectID:      projectID,
		Phase_2:        string(PromotionInstalled),
	})
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, ErrPromotionPhase
	}
	after, err := loadProject(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	if err := r.enqueueProjectEventTx(ctx, tx, api.ProjectEventUpdated, after); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	r.notifyProjectEvents()
	return after, nil
}

func (r *SQLRegistry) DeletePromotion(ctx context.Context, projectID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := r.queries.WithTx(tx)
	row, err := q.GetProjectPromotion(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrPromotionNotFound, projectID)
	}
	if err != nil {
		return err
	}
	promotion, err := promotionFromRow(row)
	if err != nil {
		return err
	}
	if promotion.Phase != PromotionCommitted {
		return fmt.Errorf("%w: have %s want %s", ErrPromotionPhase, promotion.Phase, PromotionCommitted)
	}
	if err := q.DeleteProjectPromotion(ctx, projectID); err != nil {
		return err
	}
	p, err := loadProject(ctx, q, projectID)
	if err != nil {
		return err
	}
	if err := r.enqueueProjectEventTx(ctx, tx, api.ProjectEventUpdated, p); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	r.notifyProjectEvents()
	return nil
}

func (r *SQLRegistry) CancelPromotion(ctx context.Context, projectID string) (*Project, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := r.queries.WithTx(tx)
	n, err := q.CancelProjectPromotion(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, ErrPromotionPhase
	}
	p, err := loadProject(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	if err := r.enqueueProjectEventTx(ctx, tx, api.ProjectEventUpdated, p); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	r.notifyProjectEvents()
	return p, nil
}
