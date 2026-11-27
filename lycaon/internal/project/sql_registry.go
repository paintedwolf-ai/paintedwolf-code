package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/timelayout"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/pkg/api"
)

// SQLRegistry persists projects in SQLite.
type SQLRegistry struct {
	db                   db.Handle
	queries              *db.Queries
	outbox               projectEventOutbox
	rootsChangedObserver RootsChangedObserver
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
		LastOpenedAt:    timelayout.Format(now),
		CreatedAt:       timelayout.Format(now),
		RootsGeneration: 0,
		Starred:         0,
	}); err != nil {
		return nil, fmt.Errorf("insert project: %w", err)
	}
	if err := q.CreateProjectRoot(ctx, db.CreateProjectRootParams{
		ID:        uuid.NewString(),
		ProjectID: id,
		Label:     projectroot.OutsideLabel,
		AddedAt:   timelayout.Format(now),
		Kind:      string(RootKindOutside),
	}); err != nil {
		return nil, fmt.Errorf("insert outside namespace: %w", err)
	}
	roots := make([]Root, 0, len(params.Roots))
	for _, rootIn := range params.Roots {
		root, err := attachRootTx(ctx, q, id, roots, rootIn, RootKindAttached)
		if err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	if params.Draft && len(params.Roots) == 0 {
		if err := ensureDraftScratchRoot(ctx, id, func(in AttachRootParams) (Root, error) {
			return attachRootTx(ctx, q, id, nil, in, RootKindDraft)
		}); err != nil {
			return nil, err
		}
	}
	p, err := loadProject(ctx, q, id)
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

// GetForRemoval loads a project for lifecycle removal/recovery even when an
// interrupted deletion already removed child rows (such as the outside root).
func GetForRemoval(ctx context.Context, reg interface {
	Get(context.Context, string) (*Project, error)
}, id string) (*Project, error) {
	type removalGetter interface {
		GetForRemoval(context.Context, string) (*Project, error)
	}
	if rg, ok := reg.(removalGetter); ok {
		return rg.GetForRemoval(ctx, id)
	}
	return reg.Get(ctx, id)
}

func (r *SQLRegistry) Get(ctx context.Context, id string) (*Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return loadProject(ctx, r.queries, id)
}

func (r *SQLRegistry) GetForRemoval(ctx context.Context, id string) (*Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return loadProjectForRemoval(ctx, r.queries, id)
}

func fetchListProjectRows(ctx context.Context, q *db.Queries) (
	[]db.ListProjectsRow,
	[]db.ProjectRoots,
	[]db.ListProjectOutsideRootsRow,
	[]db.ProjectPromotions,
	error,
) {
	rows, err := q.ListProjects(ctx)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	allRoots, err := q.ListAllProjectRoots(ctx)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	outsideRows, err := q.ListProjectOutsideRoots(ctx)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	promotionRows, err := q.ListProjectPromotions(ctx)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return rows, allRoots, outsideRows, promotionRows, nil
}

func (r *SQLRegistry) List(ctx context.Context) ([]Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var (
		rows          []db.ListProjectsRow
		allRoots      []db.ProjectRoots
		outsideRows   []db.ListProjectOutsideRootsRow
		promotionRows []db.ProjectPromotions
		err           error
	)
	if reader, ok := r.db.(db.ReadHandle); ok {
		tx, beginErr := reader.BeginReadTx(ctx)
		if beginErr != nil {
			return nil, beginErr
		}
		rows, allRoots, outsideRows, promotionRows, err = fetchListProjectRows(ctx, r.queries.WithTx(tx))
		_ = tx.Rollback()
	} else {
		rows, allRoots, outsideRows, promotionRows, err = fetchListProjectRows(ctx, r.queries)
	}
	if err != nil {
		return nil, err
	}
	rootsByProject := projectRootsByProjectID(allRoots)
	outside := make(map[string]string, len(outsideRows))
	for _, row := range outsideRows {
		outside[row.ProjectID] = row.ID
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
		// Delete removes child project_roots (including the outside namespace)
		// in bounded batches before deleting the projects row; omit projects
		// already mid-removal rather than exposing half-deleted entries.
		if strings.TrimSpace(outside[row.ID]) == "" {
			continue
		}
		p, err := projectFromListRow(row, rootsByProject[row.ID])
		if err != nil {
			return nil, err
		}
		p.Promotion = promotions[p.ID]
		p.Outside = outsideRoot(p.ID, outside[p.ID], p.CreatedAt)
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

// Delete removes the project's rows in bounded batches, then the project row
// with its event. Busy projects are refused before any row goes and again at
// the end; a stopped removal resumes from the rows that remain.
func (r *SQLRegistry) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.refuseBusyDelete(ctx, r.queries, r.db, id); err != nil {
		return err
	}
	if err := db.DeleteProjectRows(ctx, r.db, id); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := r.queries.WithTx(tx)
	if err := r.refuseBusyDelete(ctx, q, tx, id); err != nil {
		return err
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

func (r *SQLRegistry) refuseBusyDelete(ctx context.Context, q *db.Queries, database db.DBTX, id string) error {
	if _, err := q.GetProjectByID(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return err
	}
	if forcedLifecycle(ctx) {
		return nil
	}
	blockers, err := q.CountProjectBlockingDependents(ctx, id)
	if err != nil {
		return err
	}
	var dirtyDocuments int64
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM editor_documents WHERE project_id = ? AND dirty = 1`, id).Scan(&dirtyDocuments); err != nil {
		return err
	}
	if blockers+dirtyDocuments > 0 {
		return ErrProjectBusy
	}
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
		CoverUpdatedAt:     db.NullString(timelayout.Format(updatedAt.UTC())),
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
