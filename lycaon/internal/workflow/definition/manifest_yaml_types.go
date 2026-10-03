package definition

import (
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/sandbox"
)

type workflowFile struct {
	ID                 string                           `yaml:"id"`
	Version            string                           `yaml:"version"`
	Attach             *attachYAML                      `yaml:"attach"`
	Request            *requestYAML                     `yaml:"request"`
	Extends            string                           `yaml:"extends"`
	Name               string                           `yaml:"name"`
	Description        string                           `yaml:"description"`
	Trigger            string                           `yaml:"trigger"`
	InitialPosture     string                           `yaml:"initial_posture"`
	Icon               string                           `yaml:"icon"`
	Featured           *bool                            `yaml:"featured"`
	RequiresRepo       *bool                            `yaml:"requires_repo"`
	CoordinatorProfile string                           `yaml:"coordinator_profile"`
	SurfaceProfile     string                           `yaml:"surface_profile"`
	Agents             []agentYAML                      `yaml:"agents"`
	Gates              []string                         `yaml:"gates"`
	Rules              []string                         `yaml:"rules"`
	Topology           string                           `yaml:"topology"`
	Controls           workflowControls                 `yaml:"controls"`
	Phases             []phaseYAML                      `yaml:"phases"`
	Parameters         map[string]workflowParameterYAML `yaml:"parameters"`
	Blueprint          *blueprintYAML                   `yaml:"blueprint"`
	Presets            []manifestPresetYAML             `yaml:"presets"`
	Injects            []anchor.WorkflowInject          `yaml:"injects"`
}

type agentYAML struct {
	ID    string             `yaml:"id"`
	Tools sandbox.ToolAccess `yaml:"tools"`
	Spawn *bool              `yaml:"spawn,omitempty"`
}

type manifestPresetYAML struct {
	ID          string            `yaml:"id"`
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	Trigger     string            `yaml:"trigger"`
	Params      map[string]string `yaml:"params"`
}

type workflowParameterYAML struct {
	Type    string `yaml:"type"`
	Default string `yaml:"default"`
}

type blueprintYAML struct {
	ID string `yaml:"id"`
	// File is resolved under the blueprint convention root.
	File        string   `yaml:"file"`
	Frontmatter []string `yaml:"frontmatter"`
}

type humanApprovalYAML struct {
	// Blueprint is resolved under the blueprint convention root.
	Blueprint string `yaml:"blueprint"`
	Readiness string `yaml:"readiness"`
}

type reviewLoopYAML struct {
	ReconcilesPhase           string            `yaml:"reconciles_phase,omitempty"`
	RequireInventoryAccounted bool              `yaml:"require_inventory_accounted,omitempty"`
	IncludeScanInventory      bool              `yaml:"include_scan_inventory,omitempty"`
	EvidenceKey               string            `yaml:"evidence_key"`
	IterationCap              int               `yaml:"iteration_cap"`
	VerdictSchema             map[string]string `yaml:"verdict_schema"`
	RequiredAgents            []string          `yaml:"required_agents"`
	IfSpawnable               []string          `yaml:"if_spawnable"`
	ClaimStatuses             map[string]string `yaml:"claim_statuses,omitempty"`
	BriefLabel                string            `yaml:"brief_label,omitempty"`
}

type workflowControls struct {
	PhaseAdvance         string              `yaml:"phase_advance"`
	DefaultExecutionMode string              `yaml:"default_execution_mode"`
	OnDecisionReject     *decisionRejectYAML `yaml:"on_decision_reject"`
	OnPause              *pauseControlsYAML  `yaml:"on_pause"`
	OnStop               *stopControlsYAML   `yaml:"on_stop"`
	ContentReview        *contentReviewYAML  `yaml:"content_review"`
	Report               *reportControlsYAML `yaml:"report"`
}

type reportControlsYAML struct {
	Enabled       bool       `yaml:"enabled"`
	FindingsLabel string     `yaml:"findings_label"`
	Brief         *briefYAML `yaml:"brief,omitempty"`
}

type attachYAML struct {
	Policy string `yaml:"policy"`
}

type requestYAML struct {
	Cadence  string `yaml:"cadence,omitempty"`
	Question string `yaml:"question"`
	Default  string `yaml:"default,omitempty"`
}

type decisionRejectYAML struct {
	Pause  bool `yaml:"pause"`
	Cancel bool `yaml:"cancel"`
}

type pauseControlsYAML struct {
	HoldPending   bool `yaml:"hold_pending"`
	CancelRunning bool `yaml:"cancel_running"`
}

type stopControlsYAML struct {
	CancelWorkers   bool `yaml:"cancel_workers"`
	AbortDelegation bool `yaml:"abort_delegation"`
	SessionAbort    bool `yaml:"session_abort"`
}

type invokeWorkflowYAML struct {
	WorkflowID string `yaml:"workflow_id"`
	Version    string `yaml:"version"`
	Blueprint  string `yaml:"blueprint"`
}

type phaseYAML struct {
	ID                 string              `yaml:"id"`
	ActivityLabel      string              `yaml:"activity_label"`
	CompleteWhen       string              `yaml:"complete_when"`
	EntryWhen          string              `yaml:"entry_when"`
	InvokeWorkflow     *invokeWorkflowYAML `yaml:"invoke_workflow"`
	InvokeTrigger      string              `yaml:"invoke_trigger"`
	Next               string              `yaml:"next"`
	ChildNext          string              `yaml:"child_next"`
	ChildCompleteWhen  string              `yaml:"child_complete_when"`
	ChildGates         []string            `yaml:"child_gates"`
	OnEnter            *onEnterYAML        `yaml:"on_enter"`
	OnReenter          *onReenterYAML      `yaml:"on_reenter"`
	Controls           *phaseControlsYAML  `yaml:"controls"`
	Gates              []string            `yaml:"gates"`
	BindTopologyStage  string              `yaml:"bind_topology_stage"`
	BindParallelGroup  []string            `yaml:"bind_parallel_group"`
	ParallelTask       *parallelTaskYAML   `yaml:"parallel_task"`
	Fanout             *fanoutYAML         `yaml:"fanout"`
	Touch              *phaseTouchYAML     `yaml:"touch"`
	Terminal           bool                `yaml:"terminal"`
	CoordinatorSurface string              `yaml:"coordinator_surface"`
	SurfaceTemplate    string              `yaml:"surface_template"`
	ModeRefs           []string            `yaml:"mode_refs"`
	Advance            *phaseAdvanceYAML   `yaml:"advance"`
	Loop               *phaseLoopYAML      `yaml:"loop"`
	Intake             []string            `yaml:"intake"`
	HumanApproval      *humanApprovalYAML  `yaml:"human_approval"`
	ReviewLoop         *reviewLoopYAML     `yaml:"review_loop"`
	DepthParam         string              `yaml:"depth_param"`
	Transitions        []transitionYAML    `yaml:"transitions"`
	BlueprintWrite     bool                `yaml:"blueprint_write"`
	Explain            *explainYAML        `yaml:"explain"`
}

type explainYAML struct {
	Summary string `yaml:"summary"`
	Body    string `yaml:"body"`
}

type transitionYAML struct {
	ID     string   `yaml:"id"`
	To     string   `yaml:"to"`
	Actors []string `yaml:"actors"`
	Label  string   `yaml:"label"`
	When   string   `yaml:"when"`
}

type phaseAdvanceYAML struct {
	WhenGateMet string `yaml:"when_gate_met"`
}

type phaseLoopYAML struct {
	Exit string `yaml:"exit"`
}

type parallelTaskYAML struct {
	MaxWorkers      int `yaml:"max_workers"`
	MaxReadWorkers  int `yaml:"max_read_workers"`
	MaxWriteWorkers int `yaml:"max_write_workers"`
}

type fanoutYAML struct {
	RequireThreatModel bool `yaml:"require_threat_model"`
	MaxAttempts        int  `yaml:"max_attempts,omitempty"`
}

type phaseTouchYAML struct {
	Paths []string `yaml:"paths"`
}

type onEnterYAML struct {
	SetPosture          string            `yaml:"set_posture"`
	SetExecutionMode    string            `yaml:"set_execution_mode"`
	PromptCoordinator   bool              `yaml:"prompt_coordinator"`
	RequestUserFeedback *userFeedbackYAML `yaml:"request_user_feedback"`
	Obligations         []obligationYAML  `yaml:"obligations"`
}

type obligationYAML struct {
	Kind   string         `yaml:"kind"`
	Params map[string]any `yaml:",inline"`
}

type onReenterYAML struct {
	InjectKick string `yaml:"inject_kick"`
	ReenterLeg string `yaml:"reenter_leg"`
}

type userFeedbackYAML struct {
	Prompt       string   `yaml:"prompt"`
	ResponseType string   `yaml:"response_type"`
	Options      []string `yaml:"options"`
	AllowOther   bool     `yaml:"allow_other"`
}

type phaseControlsYAML struct {
	ContentReview *contentReviewYAML `yaml:"content_review"`
	Closeout      string             `yaml:"closeout"`
}

type contentReviewYAML struct {
	Tools []string `yaml:"tools"`
	Paths []string `yaml:"paths"`
}
