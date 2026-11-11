package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"slices"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/db"
)

// ReadActionCause reads the bounded transcript material around one tool call:
// its model output, the user turn before it and the results between them.
func (s *sqlTranscript) ReadActionCause(ctx context.Context, ref briefing.ActionRef) (briefing.CausalRead, error) {
	tx, err := s.core.db.BeginReadTx(ctx)
	if err != nil {
		return briefing.CausalRead{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.core.queries.WithTx(tx)
	before := int64(math.MaxInt64)
	if ord, err := q.BriefingToolResultOrd(ctx, db.BriefingToolResultOrdParams{SessionID: ref.SessionID, ToolCallID: db.NullString(ref.ToolCallID)}); err == nil {
		before = ord
	} else if !errors.Is(err, sql.ErrNoRows) {
		return briefing.CausalRead{}, err
	}
	output, err := q.BriefingActionOutput(ctx, db.BriefingActionOutputParams{
		ProseBytes: briefing.ProseReadChars, ReasoningCap: briefing.ReasoningReadBytes,
		SessionID: ref.SessionID, BeforeOrd: before, WorkerJobID: ref.WorkerJobID, ToolCallID: ref.ToolCallID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return briefing.CausalRead{}, tx.Commit()
	}
	if err != nil {
		return briefing.CausalRead{}, err
	}
	read := briefing.CausalRead{Found: true, OutputID: output.ID, Prose: output.Prose, ReasoningJSON: output.ReasoningPayload, ReasoningBytes: output.ReasoningBytes, CallsInOutput: int(output.CallCount)}
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
