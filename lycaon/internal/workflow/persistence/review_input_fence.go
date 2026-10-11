package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
)

func verifyReviewInputsTx(ctx context.Context, tx *sql.Tx, runID, phase string, vars map[string]any) error {
	value, ok := conditions.DotPathGet(vars, "accepted_review_subjects."+phase)
	if !ok {
		return nil
	}
	raw, ok := value.(string)
	if !ok {
		return fmt.Errorf("accepted review subject is invalid")
	}
	var accepted runstate.AcceptedReviewInputs
	if err := json.Unmarshal([]byte(raw), &accepted); err != nil {
		return err
	}
	revision, err := db.New(tx).GetWorkflowReviewInputRevision(ctx, runID)
	if err != nil {
		return err
	}
	if revision != accepted.Facts.InputRevision {
		return &toolrejection.ToolReject{Code: runstate.ReviewContextChangedCode, Data: map[string]any{"action": "refresh_context"}}
	}
	return nil
}
