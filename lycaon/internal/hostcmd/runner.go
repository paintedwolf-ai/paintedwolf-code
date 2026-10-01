package hostcmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/exec"
)

// Request runs one or more host command stages in projectDir.
type Request struct {
	ProjectDir string
	ProfileID  string
	Stages     []exec.Stage
	IOParams
	// Launch is the mandatory process authority and confinement for every stage.
	Launch exec.LaunchPlan
	// PathExtra appends catalog-resolved install directories to the child PATH.
	PathExtra []string
}

// StageResult is one stage outcome in the tool-visible JSON envelope.
type StageResult struct {
	Command string `json:"command"`
	// ExitCode is absent for skipped stages.
	ExitCode *int `json:"exit_code,omitempty"`
	// Failed is the executor's verdict on ExitCode: SIGPIPE ending a stage
	// whose reader finished first is not a failure.
	Failed bool `json:"-"`
	// Skipped means the preceding exit status did not satisfy the connector.
	Skipped bool `json:"skipped,omitempty"`
	// Connector joins this stage to its predecessor; the first stage has none.
	Connector string `json:"connector,omitempty"`
}

// StageResultsFromRun projects a finished plan into the tool-visible envelope.
func StageResultsFromRun(runs []exec.StageRun) []StageResult {
	out := make([]StageResult, len(runs))
	for i, run := range runs {
		out[i] = StageResult{
			Command:   run.Command,
			ExitCode:  run.ExitCode,
			Failed:    run.Failed,
			Skipped:   run.Skipped,
			Connector: string(run.Connector),
		}
	}
	return out
}

// FailedStages returns the command lines of the stages the executor judged
// failed. Without a recorded status per stage, as for a terminal capture, a
// nonzero exitCode fails the invocation as one.
func FailedStages(stages []StageResult, exitCode int) []string {
	var failed []string
	recorded := false
	for _, stage := range stages {
		if stage.ExitCode == nil {
			continue
		}
		recorded = true
		if stage.Failed {
			failed = append(failed, stage.Command)
		}
	}
	if recorded || exitCode == 0 {
		return failed
	}
	for _, stage := range stages {
		failed = append(failed, stage.Command)
	}
	return failed
}

// StagePlaceholders creates initial stage representations before execution.
func StagePlaceholders(stages []exec.Stage) []StageResult {
	out := make([]StageResult, len(stages))
	for i, stage := range stages {
		out[i] = StageResult{Command: stage.EchoLine()}
		if i > 0 {
			out[i].Connector = string(stage.Connector.OrPipe())
		}
	}
	return out
}

// Result is stdout/stderr summary from a host command pipeline run.
type Result struct {
	TerminationReason string        `json:"termination_reason,omitempty"`
	Stages            []StageResult `json:"stages"`
	ExitCode          int           `json:"exit_code"`
	Tail              string        `json:"tail,omitempty"`
	OK                bool          `json:"ok"`
	// ExecFailure is the run error the exit status does not carry.
	ExecFailure *ExecFailure `json:"exec_failure,omitempty"`
	// Truncated is true when Tail is not the whole retained output.
	// OriginalTailBytes is how much screened output the process left behind;
	// WireSpillPath names the host-data file holding all of it, readable by line.
	Truncated         bool   `json:"truncated,omitempty"`
	OriginalTailBytes int    `json:"original_tail_bytes,omitempty"`
	WireSpillPath     string `json:"wire_spill_path,omitempty"`
	// Network contains broker-observed destinations and their allow/deny verdicts.
	Network []confine.EgressHost `json:"network,omitempty"`
	// Report is the confinement summary every process-spawning tool states.
	confine.Report
	StdinProvided bool     `json:"stdin_provided,omitempty"`
	StdinFrom     string   `json:"stdin_from,omitempty"`
	StdoutTo      string   `json:"stdout_to,omitempty"`
	StderrTo      string   `json:"stderr_to,omitempty"`
	EnvKeys       []string `json:"env_keys,omitempty"`
	// Cwd is relative to the process root.
	Cwd string `json:"cwd,omitempty"`
	// LeftRunning counts processes remaining after command termination.
	LeftRunning int `json:"left_running,omitempty"`
	// GuidanceCodes classify a confinement refusal.
	GuidanceCodes []string `json:"-"`
	// Observation records the confinement boundary.
	Observation confine.Observation `json:"-"`
}

// BackgroundStartResult is returned when command/verify run with background:true.
type BackgroundStartResult struct {
	Background bool          `json:"background"`
	Handle     string        `json:"handle"`
	Stages     []StageResult `json:"stages"`
	// ExitedEarly marks a launch-grace failure.
	ExitedEarly bool   `json:"exited_early,omitempty"`
	ExitCode    *int   `json:"exit_code,omitempty"`
	Tail        string `json:"tail,omitempty"`
	// Network is the destinations observed during the grace window, each with its
	// allow/deny verdict. Empty when the launch settled without dialing.
	Network []confine.EgressHost `json:"network,omitempty"`
	// Report is the confinement summary every process-spawning tool states.
	confine.Report
}

// CommandRunningResult describes a command promoted to a background handle after its foreground deadline.
type CommandRunningResult struct {
	Running  bool          `json:"running"`
	Handle   string        `json:"handle"`
	Stages   []StageResult `json:"stages"`
	Tail     string        `json:"tail,omitempty"`
	WaitedMs int           `json:"waited_ms"`
	// Network contains destinations observed during process execution.
	Network []confine.EgressHost `json:"network,omitempty"`
	// Report is the confinement summary every process-spawning tool states.
	confine.Report
}

// Runner validates command pipelines prior to dispatch.
type Runner struct{}

// NewRunner constructs a host command runner.
func NewRunner() *Runner {
	return &Runner{}
}

// ValidateStages checks each pipeline stage independently.
func (r *Runner) ValidateStages(_ context.Context, stages []exec.Stage) error {
	if len(stages) == 0 {
		return fmt.Errorf("pipeline requires at least one stage")
	}
	for i, stage := range stages {
		// Only the command name is checked for shell metacharacters.
		if strings.TrimSpace(stage.Name) == "" {
			return fmt.Errorf("pipeline stage %d: %w", i, argv.ErrCommandRequired)
		}
		if argv.ContainsShellMetacharacters(stage.Name) {
			return fmt.Errorf("pipeline stage %d: %w in command name", i, argv.ErrShellMetacharacters)
		}
	}
	return exec.ValidateStreams(stages)
}
