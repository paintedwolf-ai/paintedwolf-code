package projectremoval

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/project"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Recover settles abandoned receipts without replaying deletion or extension effects.
func (o *Owner) Recover(ctx context.Context) error {
	after := ""
	for {
		ids, err := o.Store.unsettled(ctx, after)
		if err != nil || len(ids) == 0 {
			return err
		}
		for _, id := range ids {
			if err := o.recoverOperation(ctx, id); err != nil {
				return err
			}
		}
		after = ids[len(ids)-1]
	}
}

func (o *Owner) recoverOperation(ctx context.Context, id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	row, found, err := o.Store.read(ctx, id)
	if err != nil || !found {
		return err
	}
	if _, active := o.live(row.ProjectID, id); active {
		return nil
	}
	_, err = o.replay(ctx, row)
	return err
}

func (o *Owner) replay(ctx context.Context, row record) (wire.ProjectRemovalResult, error) {
	if row.Settled {
		return row.Result, nil
	}
	_, err := o.Projects.Get(ctx, row.ProjectID)
	switch {
	case errors.Is(err, project.ErrNotFound):
		row.Result.ProjectState = "deleted"
	case err != nil:
		return row.Result, err
	default:
		row.Result.ProjectState = "retained"
	}
	row.Result.Progress = nil
	row.Result.CleanupState = "interrupted"
	row.Result.FailureCode = "removal_interrupted"
	row.Result.Reason = "Removal was interrupted. Review the project and extension settings before starting another operation."
	return row.Result, o.Store.save(ctx, row.Result, true)
}

func (s *Store) unsettled(ctx context.Context, after string) ([]string, error) {
	rows, err := s.database.QueryContext(ctx, `SELECT operation_id FROM project_removals WHERE settled=0 AND operation_id>? ORDER BY operation_id LIMIT 128`, after)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
