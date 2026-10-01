package api

import (
	"time"
)

// --- Delegation ---

type HuntStrategy string

const (
	HuntStrategyFileBased     HuntStrategy = "file-based"
	HuntStrategyFeatureBased  HuntStrategy = "feature-based"
	HuntStrategyRiskBased     HuntStrategy = "risk-based"
	HuntStrategyResearchBased HuntStrategy = "research-based"
)

type InspectMode string

const (
	InspectModeStandard InspectMode = "standard"
	InspectModeTurbo    InspectMode = "turbo"
	InspectModeFull     InspectMode = "full"
)

type DelegationPhase string

const (
	DelegationPhaseSetup    DelegationPhase = "setup"
	DelegationPhaseWorker   DelegationPhase = "worker"
	DelegationPhaseCloseout DelegationPhase = "closeout"
	DelegationPhaseDone     DelegationPhase = "done"
)

type DelegationStatus string

const (
	DelegationStatusActive   DelegationStatus = "active"
	DelegationStatusDone     DelegationStatus = "done"
	DelegationStatusFailed   DelegationStatus = "failed"
	DelegationStatusAborted  DelegationStatus = "aborted"
	DelegationStatusCanceled DelegationStatus = "canceled"
)

type LegStatus string

const (
	LegStatusPending      LegStatus = "pending"
	LegStatusDispatched   LegStatus = "dispatched"
	LegStatusRunning      LegStatus = "running"
	LegStatusRetryPending LegStatus = "retry_pending"
	LegStatusComplete     LegStatus = "complete"
	LegStatusFailed       LegStatus = "failed"
	LegStatusHeld         LegStatus = "held"
	LegStatusCanceled     LegStatus = "canceled"
)

// IsTerminal reports whether the leg will not run again. An aborted
// delegation cancels every leg that had not settled.
func (s LegStatus) IsTerminal() bool {
	switch s {
	case LegStatusComplete, LegStatusFailed, LegStatusCanceled:
		return true
	case LegStatusPending, LegStatusDispatched, LegStatusRunning, LegStatusRetryPending, LegStatusHeld:
		return false
	}
	return false
}

type Leg struct {
	ID                 string        `json:"id"`
	DelegationID       string        `json:"delegation_id"`
	Title              string        `json:"title"`
	AgentType          string        `json:"agent_type,omitempty"`
	Status             LegStatus     `json:"status"`
	WorkerID           string        `json:"worker_id,omitempty"`
	ParentID           string        `json:"parent_id,omitempty"`
	DependsOn          []string      `json:"depends_on,omitempty"`
	Files              []string      `json:"files,omitempty"`
	Prompt             string        `json:"prompt,omitempty"`
	CompletionCriteria []string      `json:"completion_criteria,omitempty"`
	WorkspaceRoot      string        `json:"-"`
	WorkspaceID        string        `json:"workspace_id,omitempty"`
	Result             *WorkerResult `json:"result,omitempty"`
	CreatedAt          time.Time     `json:"created_at"`
	StartedAt          *time.Time    `json:"started_at,omitempty"`
	CompletedAt        *time.Time    `json:"completed_at,omitempty"`
}
