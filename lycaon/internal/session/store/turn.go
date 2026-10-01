package store

import "time"

// TurnOrigin identifies the machine boundary that admitted a durable turn.
type TurnOrigin string

const (
	TurnOriginUser           TurnOrigin = "user"
	TurnOriginLoopWake       TurnOrigin = "loop_wake"
	TurnOriginWorker         TurnOrigin = "worker"
	TurnOriginWorkerCloseout TurnOrigin = "worker_closeout"
	TurnOriginGroundingRetry TurnOrigin = "grounding_retry"
)

// TurnStatus is the durable execution state, independent of session chrome.
type TurnStatus string

const (
	TurnStatusRunning     TurnStatus = "running"
	TurnStatusRecovering  TurnStatus = "recovering"
	TurnStatusComplete    TurnStatus = "complete"
	TurnStatusFailed      TurnStatus = "failed"
	TurnStatusInterrupted TurnStatus = "interrupted"
)

// TurnPhase is the last durable restart boundary reached by an attempt.
type TurnPhase string

const (
	TurnPhasePreparing  TurnPhase = "preparing"
	TurnPhaseModel      TurnPhase = "model"
	TurnPhaseTools      TurnPhase = "tools"
	TurnPhaseDecision   TurnPhase = "decision"
	TurnPhaseFinalizing TurnPhase = "finalizing"
	TurnPhaseComplete   TurnPhase = "complete"
)

// TurnStart is the complete input needed to restart a turn after process loss.
type TurnStart struct {
	ID            string
	SessionID     string
	ProjectID     string
	Origin        TurnOrigin
	InputJSON     string
	SubmissionIDs []string
	WorkerJobID   string
}

// Turn is the small mutable head for append-only attempts and outputs.
type Turn struct {
	ID              string
	SessionID       string
	ProjectID       string
	Origin          TurnOrigin
	InputJSON       string
	Status          TurnStatus
	Revision        int64
	ActiveAttemptID string
	FinalOutputID   string
	ResultJSON      string
	Error           string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	CompletedAt     *time.Time
	// ProgressedAt is when the turn last did durable work: its start, a
	// checkpoint, or its finish. Recovery and resume never move it.
	ProgressedAt time.Time
}

// TurnAttempt is one fenced execution of a turn.
type TurnAttempt struct {
	ID             string
	TurnID         string
	Attempt        int
	Status         TurnStatus
	Phase          TurnPhase
	CheckpointJSON string
	Error          string
	StartedAt      time.Time
	UpdatedAt      time.Time
	CompletedAt    *time.Time
}

// TurnExecution is a committed turn and its active attempt.
type TurnExecution struct {
	Turn    Turn
	Attempt TurnAttempt
}

// ModelOutput is immutable provider output after settlement.
type ModelOutput struct {
	Scripted      bool
	ID            string
	TurnAttemptID string
	SessionID     string
	Iteration     int
	MessageID     string
	ProviderID    string
	Model         string
	Content       string
	ToolCallsJSON string
	ReasoningJSON string
	FinishReason  string
	CreatedAt     time.Time
	SettledAt     time.Time
}

// LiveModelOutput is a bounded, replaceable projection while bytes stream.
type LiveModelOutput struct {
	ID            string
	TurnAttemptID string
	SessionID     string
	Iteration     int
	MessageID     string
	Content       string
	ToolCallsJSON string
	ReasoningJSON string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// PendingModelOutputProjection awaits read-model projection.
type PendingModelOutputProjection struct {
	Output      ModelOutput
	WorkerJobID string
}
