package api

import (
	"encoding/json"
	"time"
)

type EventEnvelope struct {
	V              int             `json:"v"`
	EventID        string          `json:"event_id"`
	Cursor         string          `json:"cursor"`
	EntityRevision uint64          `json:"entity_revision,omitempty"`
	Topic          EventTopic      `json:"topic"`
	PublishedAt    time.Time       `json:"published_at"`
	Scope          EventScope      `json:"scope"`
	Data           json.RawMessage `json:"data"`
}

type EventScopeKind string

const (
	EventScopeDevice  EventScopeKind = "device"
	EventScopeProject EventScopeKind = "project"
	EventScopeSession EventScopeKind = "session"
)

type EventScope struct {
	Kind      EventScopeKind `json:"kind"`
	ProjectID string         `json:"project_id,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
}

type SessionEventAction string

const (
	SessionEventActionCreated SessionEventAction = "created"
	SessionEventActionUpdated SessionEventAction = "updated"
	SessionEventActionDeleted SessionEventAction = "deleted"
)

// SessionIdleDisposition is why a session went idle.
type SessionIdleDisposition string

const (
	// SessionIdleDispositionCompleted is a normal turn end.
	SessionIdleDispositionCompleted SessionIdleDisposition = "completed"
	// SessionIdleDispositionUserStopped is the user pressing Stop.
	SessionIdleDispositionUserStopped SessionIdleDisposition = "user_stopped"
	// SessionIdleDispositionTurnError is a caught turn failure reaching idle.
	SessionIdleDispositionTurnError SessionIdleDisposition = "turn_error"
	// SessionIdleDispositionInterrupted is the engine stopping under a live
	// turn. Nothing about the turn failed, so it must not read as an error.
	SessionIdleDispositionInterrupted SessionIdleDisposition = "interrupted"
)

// NoticeCode identifies host-rendered notice copy.
type NoticeCode string

// MessageChangeOp identifies transcript SSE mutations.
type MessageChangeOp string

const (
	MessageChangeAppend MessageChangeOp = "append"
	MessageChangePatch  MessageChangeOp = "patch"
)

// ArtifactChangeOp is what happened to a durable visual artifact.
type ArtifactChangeOp string

const (
	// ArtifactChangeOpWritten covers both a first write and a replacement.
	ArtifactChangeOpWritten ArtifactChangeOp = "written"
	// ArtifactChangeOpDeleted is the human tombstone.
	ArtifactChangeOpDeleted ArtifactChangeOp = "deleted"
)

type LLMCallStatus string

const (
	LLMCallStatusActive LLMCallStatus = "active"
	LLMCallStatusOK     LLMCallStatus = "ok"
	LLMCallStatusError  LLMCallStatus = "error"
)

// ActivityStatus is one lifecycle edge for a host-observed activity lease.
type ActivityStatus string

const (
	ActivityStatusActive ActivityStatus = "active"
	ActivityStatusDone   ActivityStatus = "done"
)

// ActivityKind names a closed host work phase; Den supplies its presentation copy.
type ActivityKind string

const (
	ActivityKindPreparingContext ActivityKind = "preparing_context"
	ActivityKindRunningTool      ActivityKind = "running_tool"
	ActivityKindStartingWorkflow ActivityKind = "starting_workflow"
	// ActivityKindAwaitingWake brackets an armed coordinator sleep the host will
	// end on its own. A sleep the person must end carries no lease.
	ActivityKindAwaitingWake ActivityKind = "awaiting_wake"
	// ActivityKindDeciding brackets a call to an available local decision engine.
	ActivityKindDeciding ActivityKind = "deciding"
)

// LLMRetryReason names why a provider request is being reissued.
type LLMRetryReason string

const (
	LLMRetryReasonStatus   LLMRetryReason = "status"
	LLMRetryReasonCapacity LLMRetryReason = "capacity"
	LLMRetryReasonHold     LLMRetryReason = "hold"
	LLMRetryReasonCooldown LLMRetryReason = "cooldown"
	// LLMRetryReasonUnreachable is a request that never reached the provider.
	LLMRetryReasonUnreachable LLMRetryReason = "unreachable"
	// LLMRetryReasonSilent is a delivered request the provider never answered.
	LLMRetryReasonSilent LLMRetryReason = "silent"
	// LLMRetryReasonEmptyCompletion is a completed response with no assistant payload.
	LLMRetryReasonEmptyCompletion LLMRetryReason = "empty_completion"
)

// PreviewEventOp names a live preview stream message.
type PreviewEventOp string

const (
	PreviewEventOpAttach PreviewEventOp = "attach"
	PreviewEventOpDetach PreviewEventOp = "detach"
	PreviewEventOpFrame  PreviewEventOp = "frame"
	PreviewEventOpAction PreviewEventOp = "action"
	PreviewEventOpState  PreviewEventOp = "state"
)

// CLIOpenAction distinguishes the two things `lycaon open` can resolve to.
type CLIOpenAction string

const (
	// CLIOpenActionOpen names a project that already exists.
	CLIOpenActionOpen CLIOpenAction = "open"
	// CLIOpenActionCreate proposes a root for a project that does not exist yet.
	CLIOpenActionCreate CLIOpenAction = "create"
)
