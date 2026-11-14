package workerrecords

import (
	"context"

	"github.com/lycaon/lycaon/internal/db"
)

// Workspace preparations a later round's frame names.
const (
	WorkspaceRetained  = "retained"
	WorkspaceRefreshed = "refreshed"
	WorkspaceReset     = "reset"
)

// RoundFrame is what every request of a worker's running round carries: its
// standing brief, the instructions the round received, and on a later round
// how its workspace was prepared and what became of its last changes.
type RoundFrame struct {
	Ordinal      int64
	Brief        string
	Instructions []Message
	// Workspace is retained, refreshed or reset; empty on a first round.
	Workspace string
	// PriorChange is landed, rejected, shelved or open for the latest
	// earlier round that changed files; empty when none did.
	PriorChange string
}

// RoundFrame reads the frame of the running round of the worker whose child
// session the actor runs in. ok is false when it has no running round.
func (s *Store) RoundFrame(ctx context.Context, actor Actor) (RoundFrame, bool, error) {
	w, err := s.Self(ctx, actor)
	if err != nil {
		return RoundFrame{}, false, err
	}
	current, err := s.q.GetUnsettledWorkerRound(ctx, w.ID)
	if db.IsNoRows(err) {
		return RoundFrame{}, false, nil
	}
	if err != nil {
		return RoundFrame{}, false, err
	}
	frame := RoundFrame{Ordinal: current.Ordinal, Brief: w.Brief}
	rows, err := s.q.ListRoundInstructionsAfter(ctx, db.ListRoundInstructionsAfterParams{WorkerID: w.ID, RoundOrdinal: nullInt(current.Ordinal), AfterSeq: 0})
	if err != nil {
		return RoundFrame{}, false, err
	}
	for _, m := range rows {
		frame.Instructions = append(frame.Instructions, messageFromRow(m))
	}
	if current.Ordinal == 1 {
		return frame, true, nil
	}
	rounds, err := s.q.ListWorkerRounds(ctx, w.ID)
	if err != nil {
		return RoundFrame{}, false, err
	}
	var prior struct{ ResultID, SettledAt string }
	for _, r := range rounds {
		if r.Ordinal < current.Ordinal && r.SettledAt.Valid && r.CheckpointID != "" {
			prior.ResultID, prior.SettledAt = r.ResultID, r.SettledAt.String
		}
	}
	if prior.ResultID != "" {
		frame.PriorChange = "open"
		if d, err := s.q.GetWorkerResultDisposition(ctx, prior.ResultID); err == nil && d.ChangeState != "" {
			frame.PriorChange = d.ChangeState
		} else if err != nil && !db.IsNoRows(err) {
			return RoundFrame{}, false, err
		}
	}
	frame.Workspace = WorkspaceRetained
	prep, err := s.q.GetLatestWorkerWorkspacePreparation(ctx, w.ID)
	if err != nil && !db.IsNoRows(err) {
		return RoundFrame{}, false, err
	}
	// A preparation made after the earlier change settled was this round's.
	if err == nil && prior.SettledAt != "" && prep.CreatedAt > prior.SettledAt {
		switch frame.PriorChange {
		case ChangeLanded:
			frame.Workspace = WorkspaceRefreshed
		case ChangeRejected:
			frame.Workspace = WorkspaceReset
		}
	}
	return frame, true, nil
}
