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
	FailureTracker *CommandFailureTracker
	// WriteRootGate handles cache writes outside default roots.
	WriteRootGate SandboxWriteRootGate
	// DeclaredCommand resolves the project's verification command.
	DeclaredCommand DeclaredVerifyCommand
}

type DeclaredVerifyCommand func(projectDir string) string

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
	tctx.VerificationCheck = true
	if t.Runner == nil {
		return "", fmt.Errorf("verify runner not configured")
	}
	if t.FailureTracker == nil {
		t.FailureTracker = NewCommandFailureTracker()
	}
	declared := t.declaredFor(tctx)
	// The declared check is compared as written, before the host expanded its globs.
	requested := tctx.RequestedArgs
	if requested == nil {
		requested = canonicalToolArgs(tctx, args)
	}
	if err := rejectDeclaredCommandOverride(declared, requested); err != nil {
		return "", err
	}
	run := t.confined()
	if toolkit.BoolArg(args, "background", false) {
		return runCommandBackground(ctx, t.Background, t.Runner, t.Boundary, args, tctx, run.sessionWriteRoots(ctx, tctx), VerifyToolName)
	}
	res, outcome, err := run.run(ctx, args, tctx)
	if err != nil {
		return "", err
	}
	if !outcome.Finished {
		return encodeCommandRunning(tctx, outcome, commandWaitBudget(args))
	}
	stampBoundaryRefusal(tctx, res)
	stampIndexWatch(tctx, outcome.IndexWatch)
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
		vr.DeadlineSeconds = int(commandTimeout(args, VerifyToolName) / time.Second)
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
func stampSourceRun(tctx tools.ToolContext, res *hostcmd.Result, verdict VerifyOutcome, outcome commandRunOutcome) {
	if tctx.Out == nil || res == nil {
		return
	}
	tctx.Out.SourceRun = &tools.SourceRunCapture{
		CheckID: tctx.ToolCallID, IsCheck: outcome.IsCheck,
		Command:        hostcmd.CommandLine(res.Stages),
		ExitCode:       res.ExitCode,
		Verdict:        string(verdict),
		SourceRevision: outcome.SourceRevision, SourceRootDigest: outcome.SourceRootDigest, Cwd: outcome.Cwd,
	}
}

func verificationRequested(args map[string]any) bool {
	requested, _ := args["verification"].(bool)
	return requested
}

// stampUnverifiableFacts records an unverifiable receipt.
func stampUnverifiableFacts(tctx tools.ToolContext, verdict VerifyOutcome) {
	if tctx.Out == nil || verdict != VerifyOutcomeUnverifiable {
		return
	}
	tctx.Out.Facts = guidance.ToolResultFacts{}.WithCode(toolrejection.VerifyUnverifiableCode).Merge(tctx.Out.Facts)
}

// confined binds this tool's wiring to the shared confined-foreground path.
func (t *VerifyTool) confined() confinedForeground {
	return confinedForeground{
		Background:     t.Background,
		FailureTracker: t.FailureTracker,
		Runner:         t.Runner,
		Boundary:       t.Boundary,
		WriteRootGate:  t.WriteRootGate,
		ToolName:       VerifyToolName,
	}
}
