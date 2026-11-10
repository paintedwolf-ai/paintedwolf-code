package command

import (
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProgramOutcome is the command tool's result for a settled program.
type ProgramOutcome struct {
	ResultVersion            int                              `json:"result_version,omitempty"`
	Outcome                  string                           `json:"outcome,omitempty"`
	ProgramID                string                           `json:"program_id"`
	State                    string                           `json:"state"`
	Verification             *ProgramVerification             `json:"verification,omitempty"`
	NonzeroExecutions        []ProgramExecutionOutcome        `json:"nonzero_executions,omitempty"`
	OmittedNonzeroExecutions int                              `json:"omitted_nonzero_executions,omitempty"`
	Status                   int                              `json:"status"`
	StopCode                 string                           `json:"stop_code,omitempty"`
	Stdout                   string                           `json:"stdout"`
	Stderr                   string                           `json:"stderr"`
	StdoutRef                string                           `json:"stdout_ref,omitempty"`
	StderrRef                string                           `json:"stderr_ref,omitempty"`
	Outputs                  []api.CommandProgramOutputWindow `json:"outputs,omitempty"`
	Truncated                bool                             `json:"truncated,omitempty"`
	ExecutedChildren         int                              `json:"executed_children"`
	CompletedEffects         int                              `json:"completed_effects"`
	UncertainEffects         int                              `json:"uncertain_effects"`
	NativeGaps               int                              `json:"native_gaps,omitempty"`
	// CPUTimeUnmeasured marks a time report whose user and sys count native
	// operations as zero.
	CPUTimeUnmeasured bool `json:"cpu_time_unmeasured,omitempty"`
	ReplayAvailable   bool `json:"replay_available"`
	// Refusal is the host decision that stopped the program; the output and
	// status of every child that ran before it stay in this outcome.
	Refusal *ProgramRefusal `json:"refusal,omitempty"`
	// SourceWindow is the observation window of a program that launched an
	// external process.
	SourceWindow *hostcmd.SourceWindow `json:"source_window,omitempty"`
}

// ProgramRefusal is a typed host refusal at the program's stop point with its
// rendered feedback.
type ProgramRefusal struct {
	Code            string                                       `json:"code"`
	Feedback        string                                       `json:"feedback"`
	Details         map[string]any                               `json:"details,omitempty"`
	DeliveryFailure *toolexecution.ProgramRefusalDeliveryFailure `json:"delivery_failure,omitempty"`
}

// ProgramVerification binds a verdict to the complete submitted program.
type ProgramVerification struct {
	Scope     string `json:"scope"`
	ProgramID string `json:"program_id"`
	SourceRef string `json:"source_ref"`
	Verdict   string `json:"verdict"`
}

// ProgramExecutionOutcome identifies a child without repeating its output body.
type ProgramExecutionOutcome struct {
	ExecutionID       string                  `json:"execution_id"`
	SourceUnitID      string                  `json:"source_unit_id"`
	Span              toolcontract.SourceSpan `json:"span"`
	State             string                  `json:"state"`
	StatusCode        *int                    `json:"status_code,omitempty"`
	OwnerStarted      bool                    `json:"owner_started"`
	OwnerInvoked      bool                    `json:"owner_invoked"`
	OutputPublishable bool                    `json:"output_publishable"`
	DetailRef         string                  `json:"detail_ref"`
	OutputRefs        []string                `json:"output_refs,omitempty"`
}
