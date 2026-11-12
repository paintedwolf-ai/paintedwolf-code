// Package workerlane keeps a worker's workspace lane across rounds: one
// Milestone 14 branch per worker, kept for corrections, rebuilt from source
// after a landing and reset after a rejection. The host chooses the
// workspace; neither agent does.
package workerlane

// Action is how the next round's workspace is obtained.
type Action string

const (
	// ActionSource runs a read round on current permitted source.
	ActionSource Action = "source"
	// ActionKeep reuses the lane as it is.
	ActionKeep Action = "keep"
	// ActionPrepare creates the worker's first lane, from FromCheckpoint
	// when the worker starts on an earlier result.
	ActionPrepare Action = "prepare"
	// ActionRefresh rebuilds the lane from current source after a landing.
	ActionRefresh Action = "refresh"
	// ActionReset rebuilds the lane from FromCheckpoint, or from source when
	// it is empty, after a rejection.
	ActionReset Action = "reset"
	// ActionSubject binds a reviewer to its sealed subject.
	ActionSubject Action = "subject"
	// ActionHold admits nothing until the reason clears.
	ActionHold Action = "hold"
)

// HoldReason names what blocks admission.
type HoldReason string

const (
	HoldRecovery          HoldReason = "recovery_required"
	HoldPromotion         HoldReason = "promotion_unsettled"
	HoldUnresolvedHolders HoldReason = "resources_unresolved"
)

// ChangeState is what happened to the last result's changes.
type ChangeState string

const (
	ChangeNone     ChangeState = ""
	ChangeLanded   ChangeState = "landed"
	ChangeRejected ChangeState = "rejected"
	ChangeShelved  ChangeState = "shelved"
)

// Facts describe a worker at admission. They come from the worker records,
// the last result and Milestone 14's lane and promotion state.
type Facts struct {
	// Write reports the round's scope.
	Write bool
	// Reviewer binds the round to a sealed subject checkpoint.
	Reviewer          bool
	SubjectCheckpoint string
	// HasLane reports a lane branch the worker already holds.
	HasLane bool
	// BaseCheckpoint is the sealed checkpoint a stacked worker starts on.
	BaseCheckpoint string
	// LastChange is the fate of the last settled result's changes.
	LastChange ChangeState
	// PriorCheckpoint is the checkpoint of the result before the rejected
	// one, when that earlier result is still unlanded in the lane.
	PriorCheckpoint string
	// PromotionUnsettled reports a landing that is applying or failed
	// without its recovery finishing.
	PromotionUnsettled bool
	// RefreshDone reports that the lane already holds the landed result,
	// because an idle refresh finished.
	RefreshDone bool
	// RecoveryRequired reports a lane a failed or interrupted round left
	// that has not recovered.
	RecoveryRequired bool
	// HoldersUnresolved reports processes, mounts or other mutation
	// holders the last settlement could not release.
	HoldersUnresolved bool
}

// Selection is the chosen workspace for the next round.
type Selection struct {
	Action         Action     `json:"action"`
	FromCheckpoint string     `json:"from_checkpoint,omitempty"`
	Hold           HoldReason `json:"hold,omitempty"`
}

// Select applies lifecycle §5. A hold is checked first: a lane that is
// recovering, landing or holding live resources admits nothing.
func Select(f Facts) Selection {
	switch {
	case f.RecoveryRequired:
		return Selection{Action: ActionHold, Hold: HoldRecovery}
	case f.HoldersUnresolved:
		return Selection{Action: ActionHold, Hold: HoldUnresolvedHolders}
	case f.PromotionUnsettled:
		return Selection{Action: ActionHold, Hold: HoldPromotion}
	}
	if f.Reviewer {
		return Selection{Action: ActionSubject, FromCheckpoint: f.SubjectCheckpoint}
	}
	if !f.Write {
		return Selection{Action: ActionSource}
	}
	if !f.HasLane {
		return Selection{Action: ActionPrepare, FromCheckpoint: f.BaseCheckpoint}
	}
	switch f.LastChange {
	case ChangeLanded:
		if f.RefreshDone {
			return Selection{Action: ActionKeep}
		}
		return Selection{Action: ActionRefresh}
	case ChangeRejected:
		return Selection{Action: ActionReset, FromCheckpoint: f.PriorCheckpoint}
	default:
		// Corrections, shelved changes, a zero-edit or failed round that
		// recovered: the lane keeps its work.
		return Selection{Action: ActionKeep}
	}
}
