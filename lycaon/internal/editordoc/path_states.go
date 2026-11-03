package editordoc

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/sourcebranch"
)

const documentStatusPageSize = 64

// DocumentPathState is accepted identity and publication metadata, without text.
type DocumentPathState struct {
	ID, RootID, Path, BaseSHA256 string
	BranchID                     sourcebranch.ID
	Revision                     int64
	DraftBytes, BaseBytes        int64
	Absent                       bool
}

// VisitUnsavedDocuments reads bounded metadata pages without opening documents
// or reconciling disk. Rows close before callbacks may call other document owners.
func (l *LifecycleService) VisitUnsavedDocuments(ctx context.Context, projectID string, branch sourcebranch.ID, rootID, path string, descendants bool, visit func(DocumentPathState) error) error {
	p, err := l.s.projectBranch(ctx, strings.TrimSpace(projectID), branch)
	if err != nil {
		return err
	}
	after := ""
	for {
		page, err := l.unsavedPathStatesPage(ctx, p.ID, p.BranchForRoot(rootID), rootID, path, after, descendants)
		if err != nil {
			return err
		}
		for _, state := range page {
			if state.BranchID != p.BranchForRoot(state.RootID) {
				continue
			}
			if err := visit(state); err != nil {
				return err
			}
		}
		if len(page) < documentStatusPageSize {
			return nil
		}
		after = page[len(page)-1].ID
	}
}

func (l *LifecycleService) unsavedPathStatesPage(ctx context.Context, projectID string, branch sourcebranch.ID, rootID, path, after string, descendants bool) ([]DocumentPathState, error) {
	query, args := unsavedPathStatesQuery(projectID, branch, rootID, path, after, descendants)
	rows, err := l.s.store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	page := make([]DocumentPathState, 0, documentStatusPageSize)
	for rows.Next() {
		var state DocumentPathState
		if err := rows.Scan(&state.ID, &state.RootID, &state.Path, &state.BranchID, &state.Revision, &state.BaseSHA256, &state.Absent, &state.DraftBytes, &state.BaseBytes); err != nil {
			return nil, err
		}
		page = append(page, state)
	}
	return page, rows.Err()
}

func unsavedPathStatesQuery(projectID string, branch sourcebranch.ID, rootID, path, after string, descendants bool) (string, []any) {
	query := `SELECT id,root_id,path,branch_id,revision,base_sha256,absent,length(CAST(draft AS BLOB)),length(CAST(base_content AS BLOB))
 FROM editor_documents WHERE project_id=? AND (dirty=1 OR diverged=1) AND id>?`
	args := []any{projectID, after}
	if rootID != "" {
		query += " AND branch_id=? AND root_id=?"
		args = append(args, branch.String(), rootID)
	}
	if path != "" {
		if descendants {
			// '/' and '0' bound the descendants under binary path ordering.
			query += " AND (path=? OR (path>=? AND path<?))"
			args = append(args, path, path+"/", path+"0")
		} else {
			query += " AND path=?"
			args = append(args, path)
		}
	}
	query += " ORDER BY id LIMIT ?"
	return query, append(args, documentStatusPageSize)
}
