package api

// WorkerState is a worker's derived state.
type WorkerState string

const (
	WorkerStatePlanned    WorkerState = "planned"
	WorkerStateWaiting    WorkerState = "waiting"
	WorkerStateWorking    WorkerState = "working"
	WorkerStateAsking     WorkerState = "asking"
	WorkerStateDelivered  WorkerState = "delivered"
	WorkerStateIdle       WorkerState = "idle"
	WorkerStateFailed     WorkerState = "failed"
	WorkerStateRecovering WorkerState = "recovering"
	WorkerStateClosed     WorkerState = "closed"
)

// WorkerStateReason qualifies a worker's state.
type WorkerStateReason string

const (
	WorkerStateReasonQueued            WorkerStateReason = "queued"
	WorkerStateReasonParked            WorkerStateReason = "parked"
	WorkerStateReasonApprovalWait      WorkerStateReason = "approval_wait"
	WorkerStateReasonPrerequisite      WorkerStateReason = "prerequisite"
	WorkerStateReasonResultUnaccounted WorkerStateReason = "result_unaccounted"
	WorkerStateReasonChangesUnresolved WorkerStateReason = "changes_unresolved"
	WorkerStateReasonNoReport          WorkerStateReason = "no_report"
	WorkerStateReasonRecoveryRequired  WorkerStateReason = "recovery_required"
	WorkerStateReasonWithdrawn         WorkerStateReason = "withdrawn"
	WorkerStateReasonSessionUnusable   WorkerStateReason = "session_unusable"
)

// WorkerResultDisposition is what became of a worker's latest result.
type WorkerResultDisposition string

const (
	WorkerResultUnaccounted WorkerResultDisposition = "unaccounted"
	WorkerResultAccounted   WorkerResultDisposition = "accounted"
	WorkerResultOpen        WorkerResultDisposition = "open"
	WorkerResultLanded      WorkerResultDisposition = "landed"
	WorkerResultRejected    WorkerResultDisposition = "rejected"
	WorkerResultShelved     WorkerResultDisposition = "shelved"
)

// WorkerMessageSenderRole names the authenticated role of a worker message sender.
type WorkerMessageSenderRole string

const (
	WorkerMessageSenderCoordinator WorkerMessageSenderRole = "coordinator"
	WorkerMessageSenderWorker      WorkerMessageSenderRole = "worker"
	WorkerMessageSenderPerson      WorkerMessageSenderRole = "person"
	WorkerMessageSenderHost        WorkerMessageSenderRole = "host"
)

// WorkerInstructionStatus describes whether a coordinator instruction reached a round.
type WorkerInstructionStatus string

const (
	WorkerInstructionStatusApplied    WorkerInstructionStatus = "applied"
	WorkerInstructionStatusQueued     WorkerInstructionStatus = "queued"
	WorkerInstructionStatusNotApplied WorkerInstructionStatus = "not_applied"
)
