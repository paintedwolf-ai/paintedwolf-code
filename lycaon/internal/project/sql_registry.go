package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/pkg/api"
)

// SQLRegistry persists projects in SQLite.
type SQLRegistry struct {
	db      db.Handle
	queries *db.Queries
	outbox  projectEventOutbox
}

func NewSQLRegistry(database db.Handle) *SQLRegistry {
	return &SQLRegistry{
		db:      database,
		queries: db.New(database),
	}
}

func (r *SQLRegistry) Create(ctx context.Context, params CreateParams) (*Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if params.Draft && len(params.Roots) > 0 {
		return nil, ErrDraftRootImmutable
	}
	now := time.Now().UTC()
	id := uuid.NewString()
	name := sql.NullString{}
	if trimmed := DefaultNameForCreate(params); trimmed != "" {
		name = db.NullString(trimmed)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := r.queries.WithTx(tx)
	if err := q.CreateProject(ctx, db.CreateProjectParams{
		ID:              id,
		Name:            name,
		LastOpenedAt:    db.FormatTime(now),
		CreatedAt:       db.FormatTime(now),
		RootsGeneration: 0,
		Starred:         0,
	}); err != nil {
		return nil, fmt.Errorf("insert project: %w", err)
	}
	p := &Project{
		ID:           id,
		Name:         name.String,
		IsDraft:      params.Draft,
		LastOpenedAt: now,
		CreatedAt:    now,
	}
	for _, rootIn := range params.Roots {
		if _, err := attachRootTx(ctx, q, p, rootIn, RootKindAttached); err != nil {
			return nil, err
		}
	}
	if params.Draft && len(params.Roots) == 0 {
		if err := ensureDraftScratchRoot(ctx, id, func(in AttachRootParams) (Root, error) {
			return attachRootTx(ctx, q, p, in, RootKindDraft)
		}); err != nil {
			return nil, err
		}
	}
	p, err = loadProject(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if err := r.enqueueProjectEventTx(ctx, tx, api.ProjectEventCreated, p); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	r.notifyProjectEvents()
	return p, nil
}

func (r *SQLRegistry) Get(ctx context.Context, id string) (*Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return loadProject(ctx, r.queries, id)
}

func (r *SQLRegistry) List(ctx context.Context) ([]Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := r.queries.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	allRoots, err := r.queries.ListAllProjectRoots(ctx)
	if err != nil {
		return nil, err
	}
	rootsByProject := projectRootsByProjectID(allRoots)
	promotionRows, err := r.queries.ListProjectPromotions(ctx)
	if err != nil {
		return nil, err
	}
	promotions := make(map[string]*Promotion, len(promotionRows))
	for _, row := range promotionRows {
		promotion, parseErr := promotionFromRow(row)
		if parseErr != nil {
			return nil, parseErr
		}
		promotions[promotion.ProjectID] = promotion
	}
	out := make([]Project, 0, len(rows))
	for _, row := range rows {
		p, err := projectFromListRow(row, rootsByProject[row.ID])
		if err != nil {
			return nil, err
		}
		p.Promotion = promotions[p.ID]
		if err := ValidateLifecycle(p); err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, nil
}

func (r *SQLRegistry) Patch(ctx context.Context, id string, params PatchParams) (*Project, error) {
	var name *string
	if params.Name != nil {
		trimmed := strings.TrimSpace(*params.Name)
		if trimmed == "" {
			return nil, ErrInvalidName
		}
		name = &trimmed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := r.queries.WithTx(tx)
	if name != nil {
		if err := q.UpdateProjectName(ctx, db.UpdateProjectNameParams{
			Name: db.NullString(*name),
			ID:   id,
		}); err != nil {
			return nil, err
		}
	}
	if params.Starred != nil {
		if err := q.UpdateProjectStarred(ctx, db.UpdateProjectStarredParams{
			Starred: boolToInt64(*params.Starred),
			ID:      id,
		}); err != nil {
			return nil, err
		}
	}
	after, err := loadProject(ctx, q, id)
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

func (r *SQLRegistry) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := r.queries.WithTx(tx)
	if _, err := q.GetProjectByID(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return err
	}
	if !forcedLifecycle(ctx) {
		blockers, err := q.CountProjectBlockingDependents(ctx, id)
		if err != nil {
			return err
		}
		var dirtyDocuments int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM editor_documents WHERE project_id = ? AND dirty = 1`, id).Scan(&dirtyDocuments); err != nil {
			return err
		}
		if blockers+dirtyDocuments > 0 {
			return ErrProjectBusy
		}
	}
	if err := q.DeleteProject(ctx, id); err != nil {
		return err
	}
	if err := r.enqueueProjectEventTx(ctx, tx, api.ProjectEventDeleted, &Project{ID: id}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	r.notifyProjectEvents()
	return nil
}

func (r *SQLRegistry) SetCover(ctx context.Context, id, artifactID, rootSessionID, source string, updatedAt time.Time) (*Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := r.queries.GetProjectByID(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := r.queries.WithTx(tx)
	if err := qtx.SetProjectCover(ctx, db.SetProjectCoverParams{
		CoverArtifactID:    db.NullString(strings.TrimSpace(artifactID)),
		CoverRootSessionID: db.NullString(strings.TrimSpace(rootSessionID)),
		CoverSource:        db.NullString(strings.TrimSpace(source)),
		CoverUpdatedAt:     db.NullString(db.FormatTime(updatedAt.UTC())),
		ID:                 id,
	}); err != nil {
		return nil, err
	}
	// Releasing and claiming the cover share one transaction.
	if err := visual.ClearProjectRefsOfKindTx(ctx, qtx, id, api.ArtifactReferenceKindProjectCover); err != nil {
		return nil, err
	}
	if err := visual.WriteRefsTx(ctx, qtx, []visual.RefWrite{{
		Kind:       api.ArtifactReferenceKindProjectCover,
		ArtifactID: strings.TrimSpace(artifactID),
		ProjectID:  id,
		SessionID:  strings.TrimSpace(rootSessionID),
	}}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}
