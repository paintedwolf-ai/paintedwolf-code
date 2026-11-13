package workerlane

import (
	"context"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/workerrecords"
)

// LaneState reads Milestone 14's state for a worker's lane and landings.
type LaneState interface {
	// PromotionUnsettled reports a landing of the job's changes that is
	// applying or failed without its recovery finishing.
	PromotionUnsettled(ctx context.Context, jobID string) (bool, error)
	// Readiness reports a lane branch that needs recovery, and one whose
	// processes, mounts or other writers were not released.
	Readiness(ctx context.Context, root string) (recoveryRequired, holdersUnresolved bool, err error)
}

// Assignment is what the next round is asked to be.
type Assignment struct {
	Write             bool
	Reviewer          bool
	SubjectCheckpoint string
	// BaseResultID names the earlier result a stacked worker starts on.
	BaseResultID string
}

// FactsFor gathers the admission facts for a worker's next round.
func (s *Service) FactsFor(ctx context.Context, state LaneState, checkpoints Checkpoints, actor workerrecords.Actor, workerID string, a Assignment) (Facts, error) {
	f := Facts{Write: a.Write, Reviewer: a.Reviewer, SubjectCheckpoint: a.SubjectCheckpoint}
	if a.BaseResultID != "" {
		h, err := s.Handle(ctx, checkpoints, actor, a.BaseResultID)
		if err != nil {
			return Facts{}, err
		}
		f.BaseCheckpoint = h.CheckpointID
	}
	prep, hasLane, err := s.records.LatestPreparation(ctx, actor, workerID)
	if err != nil {
		return Facts{}, err
	}
	f.HasLane = hasLane
	if hasLane {
		if f.RecoveryRequired, f.HoldersUnresolved, err = state.Readiness(ctx, prep.PreparationRef); err != nil {
			return Facts{}, err
		}
	}
	rounds, err := s.records.Rounds(ctx, actor, workerID)
	if err != nil {
		return Facts{}, err
	}
	changes, err := s.changeStates(ctx, actor, workerID)
	if err != nil {
		return Facts{}, err
	}
	last := -1
	for i := len(rounds) - 1; i >= 0; i-- {
		if rounds[i].SettledAt != "" && rounds[i].ResultID != "" {
			last = i
			break
		}
	}
	if last < 0 {
		return f, nil
	}
	lastRound := rounds[last]
	f.LastChange = ChangeState(changes[lastRound.ResultID])
	if f.PromotionUnsettled, err = state.PromotionUnsettled(ctx, lastRound.JobID); err != nil {
		return Facts{}, err
	}
	switch f.LastChange {
	case ChangeLanded:
		f.RefreshDone = hasLane && prep.SourceResultID == lastRound.ResultID
	case ChangeRejected:
		if hasLane && prep.SourceResultID == lastRound.ResultID {
			// The reset already happened; the lane is ready as it is.
			f.LastChange = ChangeNone
			break
		}
		f.PriorCheckpoint = priorUnlandedCheckpoint(rounds[:last], changes, prep)
	}
	return f, nil
}

// priorUnlandedCheckpoint is the latest earlier checkpoint in the current
// lane generation whose result was neither landed nor rejected.
func priorUnlandedCheckpoint(earlier []workerrecords.Round, changes map[string]string, prep workerrecords.PreparationRecord) string {
	switched, _ := db.ParseTime(prep.CreatedAt)
	for i := len(earlier) - 1; i >= 0; i-- {
		r := earlier[i]
		if created, err := db.ParseTime(r.CreatedAt); err == nil && created.Before(switched) {
			return ""
		}
		state := ChangeState(changes[r.ResultID])
		if r.CheckpointID != "" && state != ChangeLanded && state != ChangeRejected {
			return r.CheckpointID
		}
	}
	return ""
}

func (s *Service) changeStates(ctx context.Context, actor workerrecords.Actor, workerID string) (map[string]string, error) {
	dispositions, err := s.records.Dispositions(ctx, actor, workerID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(dispositions))
	for _, d := range dispositions {
		out[d.ResultID] = d.ChangeState
	}
	return out, nil
}
