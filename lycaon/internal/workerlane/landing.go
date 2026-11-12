package workerlane

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// Promoter is Milestone 14's promotion and rejection of a job's changes.
type Promoter interface {
	PromoteOverlay(ctx context.Context, sessionID, overlayID string, in api.PromoteOverlayInput) (api.WorkerMergeResult, error)
	RejectOverlay(ctx context.Context, sessionID, overlayID, reason string) (api.OverlayRejectOutcome, error)
}

// ErrActiveExecution refuses landing or rejecting a worker's result while a
// round of that worker is unsettled. Nothing is queued to apply later.
var ErrActiveExecution = errors.New("worker has an unsettled round")

// ErrNotLanded reports a promotion that did not land the result.
var ErrNotLanded = errors.New("promotion did not land the result")

// Change is one landing or rejection of a result.
type Change struct {
	Actor    workerrecords.Actor
	Op       workerrecords.Op
	ResultID string
	// Roots and ActiveRootID describe the source the lane is prepared from.
	Roots        []projectroot.RootRef
	ActiveRootID string
	// PriorCheckpoint is the earlier unlanded result's checkpoint a
	// rejection resets the lane to; empty resets to clean source.
	PriorCheckpoint string
}

// Outcome reports a change and the lane it left.
type Outcome struct {
	Disposition workerrecords.Disposition `json:"disposition"`
	Lane        Binding                   `json:"lane"`
	// LaneErr is set when the change committed but preparing the next
	// lane failed; only preparation is retried.
	LaneErr error `json:"-"`
}

// Land promotes a result's changes, records the landing, then prepares a
// fresh lane from source while the worker is idle.
func (s *Service) Land(ctx context.Context, promoter Promoter, change Change, in api.PromoteOverlayInput) (Outcome, error) {
	ref, unlock, err := s.beginChange(ctx, change)
	if err != nil {
		return Outcome{}, err
	}
	defer unlock()
	if ref.LandedChangeID == "" {
		merged, err := promoter.PromoteOverlay(ctx, change.Actor.SessionID, ref.JobID, in)
		if err != nil {
			return Outcome{}, err
		}
		if merged.Status != api.WorkerMergeStatusMerged {
			return Outcome{}, fmt.Errorf("%w: %s", ErrNotLanded, merged.Status)
		}
		if ref, err = s.records.Result(ctx, change.Actor, change.ResultID); err != nil {
			return Outcome{}, err
		}
		if ref.LandedChangeID == "" {
			return Outcome{}, ErrNotLanded
		}
	}
	d, err := s.records.SetChange(ctx, change.Actor, change.Op, change.ResultID, workerrecords.Change{
		State: workerrecords.ChangeLanded, LandedChangeID: ref.LandedChangeID,
	})
	if err != nil {
		return Outcome{}, err
	}
	lane, laneErr := s.switchLane(ctx, Request{
		Actor: change.Actor, Op: workerrecords.Op{ID: change.Op.ID + ":refresh"}, WorkerID: ref.WorkerID,
		Roots: change.Roots, ActiveRootID: change.ActiveRootID,
		SourceResultID: change.ResultID, PromotionRef: ref.LandedChangeID,
	}, Selection{Action: ActionRefresh})
	return Outcome{Disposition: d, Lane: lane, LaneErr: laneErr}, nil
}

// Reject discards a result's changes, records the rejection, then resets the
// lane to the state before that result.
func (s *Service) Reject(ctx context.Context, promoter Promoter, change Change, reason string) (Outcome, error) {
	reason = strings.TrimSpace(reason)
	ref, unlock, err := s.beginChange(ctx, change)
	if err != nil {
		return Outcome{}, err
	}
	defer unlock()
	if ref.LandedChangeID != "" {
		return Outcome{}, workerrecords.ErrDispositionFinal
	}
	rejected, err := s.recordedChange(ctx, change.Actor, ref)
	if err != nil {
		return Outcome{}, err
	}
	// A replay after the discard committed does not discard again.
	if rejected != workerrecords.ChangeRejected {
		if _, err := promoter.RejectOverlay(ctx, change.Actor.SessionID, ref.JobID, reason); err != nil {
			return Outcome{}, err
		}
	}
	d, err := s.records.SetChange(ctx, change.Actor, change.Op, change.ResultID, workerrecords.Change{State: workerrecords.ChangeRejected})
	if err != nil {
		return Outcome{}, err
	}
	lane, laneErr := s.switchLane(ctx, Request{
		Actor: change.Actor, Op: workerrecords.Op{ID: change.Op.ID + ":reset"}, WorkerID: ref.WorkerID,
		Roots: change.Roots, ActiveRootID: change.ActiveRootID, SourceResultID: change.ResultID,
	}, Selection{Action: ActionReset, FromCheckpoint: change.PriorCheckpoint})
	return Outcome{Disposition: d, Lane: lane, LaneErr: laneErr}, nil
}

// beginChange takes the worker's lane lock and refuses while a round runs.
func (s *Service) beginChange(ctx context.Context, change Change) (workerrecords.ResultRef, func(), error) {
	ref, err := s.records.Result(ctx, change.Actor, change.ResultID)
	if err != nil {
		return ref, nil, err
	}
	unlock := s.lock(ref.WorkerID)
	rounds, err := s.records.Rounds(ctx, change.Actor, ref.WorkerID)
	if err != nil {
		unlock()
		return ref, nil, err
	}
	for _, r := range rounds {
		if r.SettledAt == "" {
			unlock()
			return ref, nil, ErrActiveExecution
		}
	}
	return ref, unlock, nil
}

func (s *Service) recordedChange(ctx context.Context, actor workerrecords.Actor, ref workerrecords.ResultRef) (string, error) {
	dispositions, err := s.records.Dispositions(ctx, actor, ref.WorkerID)
	if err != nil {
		return "", err
	}
	for _, d := range dispositions {
		if d.ResultID == ref.ResultID {
			return d.ChangeState, nil
		}
	}
	return "", nil
}
