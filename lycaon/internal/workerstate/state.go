// Package workerstate derives a worker's roster state and the operations it
// admits from typed host facts. Admission, rejection recovery and orientation
// copy all read this one table, so they cannot disagree about what a worker
// in a given state allows.
package workerstate

import "slices"

// State is what the roster shows for a worker.
type State string

const (
	Planned    State = "planned"
	Waiting    State = "waiting"
	Working    State = "working"
	Asking     State = "asking"
	Delivered  State = "delivered"
	Idle       State = "idle"
	Failed     State = "failed"
	Recovering State = "recovering"
	Closed     State = "closed"
)

// Reason qualifies a state. Working carries parked, approval and queued
// reasons; the other states carry what put the worker there.
type Reason string

const (
	ReasonNone            Reason = ""
	ReasonQueued          Reason = "queued"
	ReasonParked          Reason = "parked"
	ReasonApprovalWait    Reason = "approval_wait"
	ReasonPrerequisite    Reason = "prerequisite"
	ReasonUnaccounted     Reason = "result_unaccounted"
	ReasonChangesOpen     Reason = "changes_unresolved"
	ReasonNoReport        Reason = "no_report"
	ReasonRecovery        Reason = "recovery_required"
	ReasonWithdrawn       Reason = "withdrawn"
	ReasonSessionUnusable Reason = "session_unusable"
)

// Role is how a worker is bound. A bound worker's agent and scope are sealed.
type Role string

const (
	RoleUnbound  Role = "unbound"
	RoleReviewer Role = "reviewer"
	RolePlanned  Role = "plan_bound"
)

// Operation is something a coordinator can do to a worker.
type Operation string

const (
	OpStart    Operation = "start"    // task with the planned leg's work identity
	OpWithdraw Operation = "withdraw" // worker_cancel on a planned or waiting worker
	OpInstruct Operation = "instruct"
	OpCancel   Operation = "cancel" // worker_cancel on a running or queued round
	OpAnswer   Operation = "answer"
	OpPreview  Operation = "preview"
	OpPromote  Operation = "promote"
	OpReject   Operation = "reject"
	OpShelve   Operation = "shelve"
	OpReview   Operation = "review" // task with review_of
	OpAccount  Operation = "account"
	OpRecover  Operation = "recover"
	OpHistory  Operation = "history"
)

// Round describes the worker's latest round.
type Round struct {
	Settled bool
	// Held is a round waiting on a prerequisite; it holds no model slot.
	Held bool
	// Queued is a round admitted but not yet running.
	Queued          bool
	Parked          bool
	ApprovalWait    bool
	DecisionPending bool
	// Reported is false for a round that ended without an accepted report.
	Reported bool
}

// Facts are the host facts a worker's state is derived from.
type Facts struct {
	Closed          bool
	SessionUnusable bool
	// Planned is a plan leg that has not started.
	Planned  bool
	Role     Role
	Recovery bool
	Latest   *Round
	// UnaccountedComplete is a complete result the coordinator has not
	// accounted for.
	UnaccountedComplete bool
	// OpenChanges are result changes neither landed, rejected nor shelved.
	OpenChanges bool
	// ShelvedChanges are result changes left unlanded on purpose.
	ShelvedChanges bool
}

// View is a worker's derived state and what it admits.
type View struct {
	State  State
	Reason Reason
	Ops    []Operation
}

// Admits reports whether the view allows op.
func (v View) Admits(op Operation) bool { return slices.Contains(v.Ops, op) }

// Derive maps facts to a view. Precedence follows how binding each fact is:
// a closed worker admits nothing but reading, recovery blocks everything
// else, and a live round outranks what earlier results left behind.
func Derive(f Facts) View {
	switch {
	case f.Closed || f.SessionUnusable:
		reason := ReasonWithdrawn
		if f.SessionUnusable {
			reason = ReasonSessionUnusable
		}
		return View{State: Closed, Reason: reason, Ops: []Operation{OpHistory}}
	case f.Recovery:
		return View{State: Recovering, Reason: ReasonRecovery, Ops: []Operation{OpRecover, OpHistory}}
	case f.Planned:
		return View{State: Planned, Ops: []Operation{OpStart, OpWithdraw, OpHistory}}
	}
	if r := f.Latest; r != nil && !r.Settled {
		switch {
		case r.Held:
			return View{State: Waiting, Reason: ReasonPrerequisite, Ops: []Operation{OpWithdraw, OpHistory}}
		case r.DecisionPending:
			return View{State: Asking, Ops: []Operation{OpAnswer, OpCancel, OpHistory}}
		}
		reason := ReasonNone
		switch {
		case r.ApprovalWait:
			reason = ReasonApprovalWait
		case r.Parked:
			reason = ReasonParked
		case r.Queued:
			reason = ReasonQueued
		}
		return View{State: Working, Reason: reason, Ops: []Operation{OpInstruct, OpCancel, OpHistory}}
	}
	changes := changeOps(f)
	if r := f.Latest; r != nil && !r.Reported {
		return View{State: Failed, Reason: ReasonNoReport, Ops: append([]Operation{OpInstruct}, append(changes, OpHistory)...)}
	}
	if f.UnaccountedComplete || f.OpenChanges {
		reason := ReasonChangesOpen
		if f.UnaccountedComplete {
			reason = ReasonUnaccounted
		}
		ops := []Operation{OpInstruct}
		ops = append(ops, changes...)
		ops = append(ops, OpReview)
		if f.UnaccountedComplete {
			ops = append(ops, OpAccount)
		}
		return View{State: Delivered, Reason: reason, Ops: append(ops, OpHistory)}
	}
	return View{State: Idle, Ops: append([]Operation{OpInstruct}, append(changes, OpHistory)...)}
}

// changeOps are the operations on a worker's unlanded changes: open changes
// admit all four, shelved ones can still be landed or rejected.
func changeOps(f Facts) []Operation {
	switch {
	case f.OpenChanges:
		return []Operation{OpPreview, OpPromote, OpReject, OpShelve}
	case f.ShelvedChanges:
		return []Operation{OpPreview, OpPromote, OpReject}
	}
	return nil
}

// AgentChange is why an instruction may not change a worker's agent or
// scope; the zero value admits the change.
type AgentChange string

const (
	AgentChangeAdmitted    AgentChange = ""
	AgentChangeBusy        AgentChange = "not_between_rounds"
	AgentChangeSealed      AgentChange = "sealed_role"
	AgentChangeOpenChanges AgentChange = "unresolved_changes"
)

// CheckAgentChange decides whether an instruction may move a worker to
// another agent. A change from write to read leaves no lane for changes that
// are not landed or rejected, shelved ones included.
func CheckAgentChange(v View, f Facts, fromWrite, toWrite bool) AgentChange {
	switch {
	case f.Role == RoleReviewer || f.Role == RolePlanned:
		return AgentChangeSealed
	case v.State == Working || !v.Admits(OpInstruct):
		return AgentChangeBusy
	case fromWrite && !toWrite && (f.OpenChanges || f.ShelvedChanges):
		return AgentChangeOpenChanges
	}
	return AgentChangeAdmitted
}
