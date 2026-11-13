// Package briefingstore persists briefing state and reads its evidence from
// the store.
package briefingstore

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"slices"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/db"
)

// Causal reads action evidence from the transcript.
type Causal struct {
	handle  db.ReadHandle
	queries *db.Queries
}

func NewCausal(handle db.ReadHandle) *Causal {
	return &Causal{handle: handle, queries: db.New(handle)}
}

// ReadActionCause reads the bounded transcript material around one tool call:
// its model output, the user turn before it and the results between them.
func (c *Causal) ReadActionCause(ctx context.Context, ref briefing.ActionRef) (briefing.CausalRead, error) {
	tx, err := c.handle.BeginReadTx(ctx)
	if err != nil {
		return briefing.CausalRead{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := c.queries.WithTx(tx)
	before := int64(math.MaxInt64)
	if ord, err := q.BriefingToolResultOrd(ctx, db.BriefingToolResultOrdParams{SessionID: ref.SessionID, ToolCallID: db.NullString(ref.ToolCallID)}); err == nil {
		before = ord
	} else if !errors.Is(err, sql.ErrNoRows) {
		return briefing.CausalRead{}, err
	}
	output, err := q.BriefingActionOutput(ctx, db.BriefingActionOutputParams{ArgsBytes: briefing.ArgsReadChars,
		ProseBytes: briefing.ProseReadChars, ReasoningCap: briefing.ReasoningReadBytes,
		SessionID: ref.SessionID, BeforeOrd: before, WorkerJobID: ref.WorkerJobID, ToolCallID: ref.ToolCallID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return briefing.CausalRead{}, tx.Commit()
	}
	if err != nil {
		return briefing.CausalRead{}, err
	}
	read := briefing.CausalRead{Found: true, OutputID: output.ID, Prose: output.Prose, ReasoningJSON: output.ReasoningPayload, ReasoningBytes: output.ReasoningBytes, CallsInOutput: int(output.CallCount),
		Call: briefing.CallExcerpt{Name: output.CallName, Args: output.CallArgs}}
	result, err := q.BriefingCallResult(ctx, db.BriefingCallResultParams{ContentBytes: briefing.ResultReadChars, SessionID: ref.SessionID, ToolCallID: db.NullString(ref.ToolCallID)})
	switch {
	case err == nil:
		read.Call.Result = &briefing.ResultExcerpt{Tool: result.Tool, Outcome: result.Outcome, Content: result.Content, Ref: "message:" + result.ID}
	case !errors.Is(err, sql.ErrNoRows):
		return briefing.CausalRead{}, err
	}
	after := int64(-1)
	intent, err := q.BriefingUserIntentBefore(ctx, db.BriefingUserIntentBeforeParams{IntentBytes: briefing.IntentReadChars, SessionID: ref.SessionID, BeforeOrd: output.Ord})
	switch {
	case err == nil:
		read.IntentID, read.Intent, after = intent.ID, intent.Intent, intent.Ord
	case !errors.Is(err, sql.ErrNoRows):
		return briefing.CausalRead{}, err
	}
	results, err := q.BriefingResultsBetween(ctx, db.BriefingResultsBetweenParams{ContentBytes: briefing.ResultReadChars, SessionID: ref.SessionID, AfterOrd: after, BeforeOrd: output.Ord})
	if err != nil {
		return briefing.CausalRead{}, err
	}
	for _, row := range slices.Backward(results) {
		read.Results = append(read.Results, briefing.ResultExcerpt{Tool: row.Tool, Outcome: row.Outcome, Content: row.Content, Ref: "message:" + row.ID})
	}
	return read, tx.Commit()
}
