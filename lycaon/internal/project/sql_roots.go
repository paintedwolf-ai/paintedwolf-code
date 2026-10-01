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
	"github.com/lycaon/lycaon/pkg/api"
)

// RootPaths returns the path of every root of every registered project.
func (r *SQLRegistry) RootPaths(ctx context.Context) ([]string, error) {
	rows, err := r.queries.ListAllProjectRoots(ctx)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(rows))
	for _, row := range rows {
		paths = append(paths, row.Path)
	}
	return paths, nil
}

func (r *SQLRegistry) AttachRoot(ctx context.Context, id string, params AttachRootParams) (*RootChange, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := r.queries.WithTx(tx)
	before, err := loadProject(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if before.IsDraft {
		return nil, ErrDraftRootImmutable
	}
	blockers, err := q.CountProjectBlockingDependents(ctx, id)
	if err != nil {
		return nil, err
	}
	if blockers > 0 {
		return nil, ErrProjectBusy
	}
	added, err := attachRootTx(ctx, q, before, params, RootKindAttached)
	if err != nil {
		return nil, err
	}
	if err := q.BumpProjectRootsGeneration(ctx, id); err != nil {
		return nil, err
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
	return &RootChange{
		Before: before, After: after, Added: &added, Changed: true, RootContextChanged: true,
	}, nil
}

func (r *SQLRegistry) DetachRoot(ctx context.Context, id, rootID string) (*RootChange, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := r.queries.WithTx(tx)
	before, err := loadProject(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if before.IsDraft {
		return nil, ErrDraftRootImmutable
	}
	rootRow, err := q.GetProjectRootByID(ctx, db.GetProjectRootByIDParams{
		ID:        rootID,
		ProjectID: id,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ErrRootNotFound, rootID)
		}
		return nil, err
	}
	removed, ok := rootByID(before.Roots, rootID)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrRootNotFound, rootID)
	}
	allRoots, err := q.ListProjectRoots(ctx, id)
	if err != nil {
		return nil, err
	}
	if !forcedLifecycle(ctx) {
		var blockers int64
		if len(allRoots) > 1 {
			if err := tx.QueryRowContext(ctx, `SELECT (
                    SELECT COUNT(*) FROM sessions WHERE project_id = ? AND status = 'busy'
                ) + (
                    SELECT COUNT(*) FROM worker_jobs WHERE project_id = ? AND (
                        status IN ('pending', 'running', 'waiting', 'held')
                        OR (status = 'complete' AND merge_status = 'pending')
                    )
                )`, id, id).Scan(&blockers); err != nil {
				return nil, err
			}
		} else {
			blockers, err = q.CountRootBlockingDependents(ctx, db.CountRootBlockingDependentsParams{
				ProjectID: id,
				RootID:    db.NullString(rootID),
			})
			if err != nil {
				return nil, err
			}
		}
		var dirtyDocuments int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM editor_documents WHERE project_id = ? AND root_id = ? AND dirty = 1`, id, rootID).Scan(&dirtyDocuments); err != nil {
			return nil, err
		}
		if blockers+dirtyDocuments > 0 {
			return nil, ErrRootBusy
		}
	}
	wasPrimary := rootRow.IsPrimary != 0
	remaining := make([]db.ProjectRoots, 0, len(allRoots)-1)
	for _, row := range allRoots {
		if row.ID != rootID {
			remaining = append(remaining, row)
		}
	}
	var replacement db.ProjectRoots
	for _, row := range remaining {
		if row.IsPrimary != 0 {
			replacement = row
			break
		}
	}
	if replacement.ID == "" && len(remaining) > 0 {
		replacement = remaining[0]
		for _, row := range remaining[1:] {
			if row.AddedAt < replacement.AddedAt {
				replacement = row
			}
		}
	}
	if wasPrimary && replacement.ID != "" {
		if err := q.ClearProjectPrimary(ctx, id); err != nil {
			return nil, err
		}
		if err := q.SetProjectRootPrimary(ctx, db.SetProjectRootPrimaryParams{
			ID:        replacement.ID,
			ProjectID: id,
		}); err != nil {
			return nil, err
		}
	}
	var replacementID sql.NullString
	if replacement.ID != "" {
		replacementID = db.NullString(replacement.ID)
	}
	if err := q.ReassignSessionsWorkspaceRoot(ctx, db.ReassignSessionsWorkspaceRootParams{
		WorkspaceRootID:   replacementID,
		UpdatedAt:         db.FormatTime(time.Now().UTC()),
		ProjectID:         id,
		WorkspaceRootID_2: db.NullString(rootID),
	}); err != nil {
		return nil, err
	}
	if err := q.DeleteProjectRoot(ctx, db.DeleteProjectRootParams{
		ID:        rootID,
		ProjectID: id,
	}); err != nil {
		return nil, err
	}
	if err := q.BumpProjectRootsGeneration(ctx, id); err != nil {
		return nil, err
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
	return &RootChange{
		Before: before, After: after, Removed: &removed, Changed: true, RootContextChanged: true,
	}, nil
}

func (r *SQLRegistry) PatchRoot(ctx context.Context, id, rootID string, params PatchRootParams) (*RootChange, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if params.IsPrimary == nil && params.Label == nil {
		p, err := r.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if _, ok := rootByID(p.Roots, rootID); !ok {
			return nil, fmt.Errorf("%w: %s", ErrRootNotFound, rootID)
		}
		return &RootChange{Before: p, After: cloneProject(p)}, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := r.queries.WithTx(tx)
	before, err := loadProject(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if before.IsDraft {
		return nil, ErrDraftRootImmutable
	}
	blockers, err := q.CountProjectBlockingDependents(ctx, id)
	if err != nil {
		return nil, err
	}
	if blockers > 0 {
		return nil, ErrProjectBusy
	}
	if _, err := q.GetProjectRootByID(ctx, db.GetProjectRootByIDParams{
		ID:        rootID,
		ProjectID: id,
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ErrRootNotFound, rootID)
		}
		return nil, err
	}
	rootContextChanged := false
	changed := false
	current, _ := rootByID(before.Roots, rootID)
	if params.Label != nil {
		normalized, err := NormalizeRootDisplayLabel(*params.Label)
		if err != nil {
			return nil, err
		}
		rows, err := q.ListProjectRoots(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.ID != rootID && strings.EqualFold(row.Label, normalized) {
				return nil, fmt.Errorf("%w: %s", ErrDuplicateRootLabel, normalized)
			}
		}
		if current.Label != normalized {
			if err := q.UpdateProjectRootLabel(ctx, db.UpdateProjectRootLabelParams{
				Label:     normalized,
				ID:        rootID,
				ProjectID: id,
			}); err != nil {
				if db.IsUniqueConstraint(err) {
					return nil, fmt.Errorf("%w: %s", ErrDuplicateRootLabel, normalized)
				}
				return nil, err
			}
			changed = true
		}
	}
	if params.IsPrimary != nil && *params.IsPrimary && !current.IsPrimary {
		if err := q.ClearProjectPrimary(ctx, id); err != nil {
			return nil, err
		}
		if err := q.SetProjectRootPrimary(ctx, db.SetProjectRootPrimaryParams{
			ID:        rootID,
			ProjectID: id,
		}); err != nil {
			return nil, err
		}
		changed = true
		rootContextChanged = true
	}
	if !changed {
		return &RootChange{Before: before, After: cloneProject(before)}, nil
	}
	if rootContextChanged {
		if err := q.BumpProjectRootsGeneration(ctx, id); err != nil {
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
	return &RootChange{
		Before: before, After: after, Changed: true, RootContextChanged: rootContextChanged,
	}, nil
}

func attachRootTx(ctx context.Context, q *db.Queries, p *Project, params AttachRootParams, kind RootKind) (Root, error) {
	abs, err := ResolveExistingDir(params.Path)
	if err != nil {
		return Root{}, err
	}
	if err := defaultOpenPolicy.ValidateOpenPath(abs); err != nil {
		return Root{}, err
	}
	if _, err := q.GetProjectRootByPath(ctx, db.GetProjectRootByPathParams{
		ProjectID: p.ID,
		Path:      abs,
	}); err == nil {
		return Root{}, fmt.Errorf("%w: %s", ErrDuplicateRoot, abs)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Root{}, err
	}
	count, err := q.CountProjectRoots(ctx, p.ID)
	if err != nil {
		return Root{}, err
	}
	wantPrimary := count == 0 || params.IsPrimary != nil && *params.IsPrimary
	if wantPrimary && count > 0 {
		if err := q.ClearProjectPrimary(ctx, p.ID); err != nil {
			return Root{}, err
		}
		for i := range p.Roots {
			p.Roots[i].IsPrimary = false
		}
	}
	now := time.Now().UTC()
	rootLabel := strings.TrimSpace(params.Label)
	if rootLabel == "" {
		existing := make([]string, 0, len(p.Roots))
		for _, r := range p.Roots {
			existing = append(existing, r.Label)
		}
		rootLabel = deriveUniqueRootLabel(existing, abs)
	} else {
		normalized, err := NormalizeRootDisplayLabel(rootLabel)
		if err != nil {
			return Root{}, err
		}
		if rootLabelTaken(p.Roots, normalized, "") {
			return Root{}, fmt.Errorf("%w: %s", ErrDuplicateRootLabel, normalized)
		}
		rootLabel = normalized
	}
	root := Root{
		ID:            uuid.NewString(),
		ProjectID:     p.ID,
		Path:          abs,
		Label:         rootLabel,
		IsPrimary:     wantPrimary,
		GitRemoteHash: gitRemoteHash(ctx, abs),
		AddedAt:       now,
		Kind:          kind,
	}
	if err := q.CreateProjectRoot(ctx, db.CreateProjectRootParams{
		ID:            root.ID,
		ProjectID:     root.ProjectID,
		Path:          root.Path,
		Label:         root.Label,
		IsPrimary:     boolToInt64(root.IsPrimary),
		GitRemoteHash: db.NullString(root.GitRemoteHash),
		AddedAt:       db.FormatTime(root.AddedAt),
		Kind:          string(root.Kind),
	}); err != nil {
		if db.IsUniqueConstraint(err) {
			return Root{}, fmt.Errorf("%w: %s", ErrDuplicateRoot, abs)
		}
		return Root{}, err
	}
	p.Roots = append(p.Roots, root)
	return root, nil
}

func projectRootsByProjectID(all []db.ProjectRoots) map[string][]db.ProjectRoots {
	out := make(map[string][]db.ProjectRoots)
	for _, row := range all {
		out[row.ProjectID] = append(out[row.ProjectID], row)
	}
	return out
}

func rootsFromRows(rows []db.ProjectRoots) ([]Root, error) {
	out := make([]Root, 0, len(rows))
	for _, row := range rows {
		addedAt, err := db.ParseTime(row.AddedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, Root{
			ID:            row.ID,
			ProjectID:     row.ProjectID,
			Path:          row.Path,
			Label:         row.Label,
			IsPrimary:     row.IsPrimary != 0,
			GitRemoteHash: db.StringFromNull(row.GitRemoteHash),
			AddedAt:       addedAt,
			Kind:          RootKind(row.Kind),
		})
	}
	return out, nil
}
