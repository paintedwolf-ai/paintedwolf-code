package native

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// VerifyToolName is the tool identity the deadline and write-scope paths read.
const VerifyToolName = "verify"

type VerifyTool struct {
	Runner         *hostcmd.Runner
	Boundary       *sandbox.Boundary
	Background     *bgprocess.Registry
	FailureTracker *command.CommandFailureTracker
	// WriteRootGate handles cache writes outside default roots.
	WriteRootGate command.SandboxWriteRootGate
	// DeclaredCommand resolves the project's verification command.
	DeclaredCommand command.DeclaredVerifyCommand
}

func (t *VerifyTool) declaredFor(tctx tools.ToolContext) string {
	if t == nil || t.DeclaredCommand == nil {
		return ""
	}
	return strings.TrimSpace(t.DeclaredCommand(tctx.ActiveRootPath()))
}

type VerifyOutcome string

const (
	VerifyOutcomePassed       VerifyOutcome = api.SourceVerdictPassed
	VerifyOutcomeFailed       VerifyOutcome = api.SourceVerdictFailed
	VerifyOutcomeUnverifiable VerifyOutcome = api.SourceVerdictUnverifiable
)

type verifyResult struct {
	TerminationReason string                `json:"termination_reason,omitempty"`
	Stages            []hostcmd.StageResult `json:"stages"`
	ExitCode          int                   `json:"exit_code"`
	Outcome           VerifyOutcome         `json:"outcome"`
	// UnverifiableReason names the fact that withheld a verdict.
	UnverifiableReason string `json:"unverifiable_reason,omitempty"`
	// DeadlineSeconds is the deadline applied by the host when it ended the run.
	DeadlineSeconds int `json:"deadline_seconds,omitempty"`
	// SocksProxy is true when the process received ALL_PROXY.
	SocksProxy        bool   `json:"socks_proxy,omitempty"`
	Declared          bool   `json:"declared"`
	Tail              string `json:"tail,omitempty"`
	Truncated         bool   `json:"truncated,omitempty"`
	OriginalTailBytes int    `json:"original_tail_bytes,omitempty"`
	WireSpillPath     string `json:"wire_spill_path,omitempty"`
	confine.Report
}

const (
	unverifiableReasonBoundary = "boundary_refused"
	// unverifiableReasonDeadline marks a run ended at the host deadline.
	unverifiableReasonDeadline = "host_deadline"
	// unverifiableReasonStopped marks a run stopped by the host.
	unverifiableReasonStopped = "host_stopped"
)

// verdictFor maps execution termination to verification outcome.
func verdictFor(res *hostcmd.Result) (VerifyOutcome, string) {
	if res == nil {
		return VerifyOutcomeUnverifiable, unverifiableReasonBoundary
	}
	if res.BoundaryRefusal != "" {
		return VerifyOutcomeUnverifiable, unverifiableReasonBoundary
	}
	switch res.TerminationReason {
	case string(bgprocess.TerminationTimedOut):
		return VerifyOutcomeUnverifiable, unverifiableReasonDeadline
	case string(bgprocess.TerminationStopped):
		return VerifyOutcomeUnverifiable, unverifiableReasonStopped
	}
	if res.OK {
		return VerifyOutcomePassed, ""
	}
	return VerifyOutcomeFailed, ""
}

func (t *VerifyTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	tctx.Execution.VerificationCheck = true
	if t.Runner == nil {
		return "", fmt.Errorf("verify runner not configured")
	}
	if t.FailureTracker == nil {
		t.FailureTracker = command.NewCommandFailureTracker()
	}
	declared := t.declaredFor(tctx)
	// The declared check is compared as written, before the host expanded its globs.
	requested := tctx.Effects.RequestedArgs
	if requested == nil {
		requested = command.CanonicalToolArgs(tctx, args)
	}
	if err := rejectDeclaredCommandOverride(declared, requested); err != nil {
		return "", err
	}
	run := t.confined()
	if toolkit.BoolArg(args, "background", false) {
		return command.RunBackground(ctx, t.Background, t.Runner, t.Boundary, args, tctx, run.SessionWriteRoots(ctx, tctx), VerifyToolName)
	}
	res, outcome, err := run.Run(ctx, args, tctx)
	if err != nil {
		return "", err
	}
	if !outcome.Finished {
		return command.EncodeCommandRunning(tctx, outcome, command.WaitBudget(args))
	}
	command.StampBoundaryRefusal(tctx, res)
	command.StampIndexWatch(tctx, outcome.IndexWatch)
	tail := res.Tail
	verdict, reason := verdictFor(res)
	stampSourceRun(tctx, res, verdict, outcome)
	stampUnverifiableFacts(tctx, verdict)
	vr := verifyResult{
		TerminationReason:  res.TerminationReason,
		Stages:             res.Stages,
		ExitCode:           res.ExitCode,
		Outcome:            verdict,
		UnverifiableReason: reason,
		SocksProxy:         res.SocksProxy,
		Declared:           declared != "",
		Tail:               tail,
		Truncated:          res.Truncated,
		OriginalTailBytes:  res.OriginalTailBytes,
		WireSpillPath:      res.WireSpillPath,
		Report:             res.Report,
	}
	if reason == unverifiableReasonDeadline {
		vr.DeadlineSeconds = int(command.CommandTimeout(args, VerifyToolName) / time.Second)
	}
	if capped, truncated, orig := CapOpaqueTail(tail, 0); truncated {
		vr.Tail = capped
		vr.Truncated = true
		vr.OriginalTailBytes = max(vr.OriginalTailBytes, orig)
	}
	out, err := surveyjson.Marshal(vr)
	if err != nil {
		return "", fmt.Errorf("verify encode: %w", err)
	}
	return string(out), nil
}

// rejectDeclaredCommandOverride refuses a command that is not the declared project check.
func rejectDeclaredCommandOverride(declared string, args map[string]any) error {
	if declared == "" {
		return nil
	}
	requested := commandsurface.PrimaryCommandLine(args, nil)
	if requested == "" {
		// Empty command runs the declared check.
		return nil
	}
	if commandsurface.SameCommandLine(requested, declared) && args["pipeline"] == nil {
		return nil
	}
	return &toolrejection.ToolReject{Code: "VERIFY_DECLARED_COMMAND_OVERRIDE", Data: map[string]any{
		"declared":  declared,
		"requested": requested,
	}}
}

// stampSourceRun attaches terminal evidence to the invocation receipt.
func stampSourceRun(tctx tools.ToolContext, res *hostcmd.Result, verdict VerifyOutcome, outcome command.RunOutcome) {
	command.StampSourceRun(tctx, res, string(verdict), outcome)
}

func verificationRequested(args map[string]any) bool {
	return command.VerificationRequested(args)
}

// stampUnverifiableFacts records an unverifiable receipt.
func stampUnverifiableFacts(tctx tools.ToolContext, verdict VerifyOutcome) {
	if tctx.Effects.Out == nil || verdict != VerifyOutcomeUnverifiable {
		return
	}
	tctx.Effects.Out.Facts = guidance.ToolResultFacts{}.WithCode(toolrejection.VerifyUnverifiableCode).Merge(tctx.Effects.Out.Facts)
}

// confined binds this tool's wiring to the shared confined-foreground path.
func (t *VerifyTool) confined() command.ConfinedForeground {
	return command.ConfinedForeground{
		Background:     t.Background,
		FailureTracker: t.FailureTracker,
		Runner:         t.Runner,
		Boundary:       t.Boundary,
		WriteRootGate:  t.WriteRootGate,
		ToolName:       VerifyToolName,
	}
}
