package native

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/packageexec"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	terminaltool "github.com/lycaon/lycaon/internal/tools/native/terminal"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

const coordinatorProfileID = "coordinator"

// WriteTool writes file contents atomically within the sandbox.
type WriteTool struct {
	Boundary     *sandbox.Boundary
	ContentApply ContentApplyGate
}

// ContentApplyGate holds a proposed write for review when policy requires it and returns the content to write.
type ContentApplyGate interface {
	GateApply(ctx context.Context, tool, path string, before *string, after string, tctx tools.ToolContext) (string, error)
}

func (t *WriteTool) Name() string { return "write" }

func (t *WriteTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	var overrideErr error
	ctx, overrideErr = tools.WithSyntaxOverride(ctx, args)
	if overrideErr != nil {
		return "", overrideErr
	}
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)
	appendMode, _ := args["append"].(bool)
	if path == "" {
		return "", toolkit.MissingArg("path")
	}
	if err := assertProfileWriteScope(ctx, t.Boundary, tctx, path, "write"); err != nil {
		return "", writeScopeReject(ctx, t.Boundary, path, tctx.ProfileID(), "write", err)
	}
	if err := beforeWorkerMutation(ctx, tctx, path); err != nil {
		return "", err
	}
	resolved, err := projectpaths.ResolveWrite(ctx, t.Boundary, tctx, path)
	if err != nil {
		return "", err
	}
	fullPath := resolved.Abs
	path = resolved.DisplayPath
	return retryEditorDocument(ctx, path, func() (string, error) {
		// A missing file is a create; otherwise the base is the served source text.
		var before *string
		st, err := loadAgentSourceText(ctx, "write", tctx, resolved)
		if err == nil {
			beforeText := st.Content
			before = &beforeText
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("read failed: %w", err)
		}
		proposed := content
		if appendMode {
			if before == nil {
				return "", sourceview.PathNotFound("write", path, fullPath)
			}
			proposed = *before + content
		}
		if err := guardMutationContent("write", path, proposed); err != nil {
			return "", err
		}
		finalContent := proposed
		reportTextIntent(tctx, resolved, st, before != nil, proposed)
		if t.ContentApply != nil {
			var err error
			finalContent, err = t.ContentApply.GateApply(ctx, "write", path, before, proposed, tctx)
			if err != nil {
				return "", err
			}
			if finalContent != proposed {
				reportTextIntent(tctx, resolved, st, before != nil, finalContent)
			}
		}
		if err := rejectIfSyntaxUnhealthy(ctx, "write", path, before, finalContent, mutationSeam{}); err != nil {
			return "", err
		}
		landed, err := landEditedText(ctx, tctx, "write", resolved, st, finalContent)
		if err != nil {
			return "", err
		}
		captureFileEdit(tctx, path, finalContent, before)
		afterSuccessfulMutation(ctx, tctx, path)
		var receipt string
		if appendMode {
			receipt = fmt.Sprintf("Appended %d bytes to %s (file now %d bytes)", len(content), path, len(finalContent))
		} else {
			receipt = fmt.Sprintf("Wrote %d bytes to %s", len(finalContent), path)
		}
		if note := landed.note(); note != "" {
			receipt += "\n" + note
		}
		return receipt, nil
	})
}

// EditTool replaces one occurrence of old_string with new_string.
type EditTool struct {
	Boundary     *sandbox.Boundary
	ContentApply ContentApplyGate
}

func (t *EditTool) Name() string { return "edit" }

func (t *EditTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	var overrideErr error
	ctx, overrideErr = tools.WithSyntaxOverride(ctx, args)
	if overrideErr != nil {
		return "", overrideErr
	}
	path, _ := args["path"].(string)
	oldString, _ := args["old_string"].(string)
	newString, _ := args["new_string"].(string)
	if path == "" {
		return "", toolkit.MissingArg("path")
	}
	if oldString == "" {
		return "", toolkit.MissingArg("old_string")
	}
	if _, hasNew := args["new_string"]; !hasNew {
		return "", toolkit.MissingArg("new_string")
	}
	if oldString == newString {
		return "", editArgsConflict("old_string and new_string must differ")
	}
	if err := assertProfileWriteScope(ctx, t.Boundary, tctx, path, "edit"); err != nil {
		return "", writeScopeReject(ctx, t.Boundary, path, tctx.ProfileID(), "edit", err)
	}
	if err := beforeWorkerMutation(ctx, tctx, path); err != nil {
		return "", err
	}
	resolved, err := projectpaths.ResolveWrite(ctx, t.Boundary, tctx, path)
	if err != nil {
		return "", err
	}
	fullPath := resolved.Abs
	path = resolved.DisplayPath
	replaceAll := toolkit.BoolArg(args, "replace_all", false)
	return retryEditorDocument(ctx, path, func() (string, error) {
		st, err := loadAgentSourceText(ctx, "edit", tctx, resolved)
		if err != nil {
			if os.IsNotExist(err) {
				return "", sourceview.PathNotFound("edit", path, fullPath)
			}
			return "", fmt.Errorf("read failed: %w", err)
		}
		old := st.Content
		occurrences := strings.Count(old, oldString)
		switch {
		case occurrences == 0:
			if changes, total, ok := sourceview.ForeignChanges(ctx, tctx, fullPath); ok {
				return "", editTargetChangedByOthers(path, old, oldString, changes, total)
			}
			return "", editOldStringNotFound(path, old, oldString)
		case occurrences > 1 && !replaceAll:
			return "", editOldStringAmbiguous(path, occurrences)
		}
		replaced := 1
		newContent := strings.Replace(old, oldString, newString, 1)
		// Multiple replacements have disjoint seams; zero selects the first diagnostic.
		seamStartLine := strings.Count(old[:strings.Index(old, oldString)], "\n") + 1 //nolint:gocritic // the occurrences == 0 case returned above, so Index cannot be -1
		if replaceAll {
			replaced = occurrences
			newContent = strings.ReplaceAll(old, oldString, newString)
			if occurrences > 1 {
				seamStartLine = 0
			}
		}
		return t.finishEdit(ctx, path, resolved, st, old, newContent, tctx, replaced, len(oldString), len(newString), seamStartLine, toolkit.CountLines(newString))
	})
}

func captureFileEdit(tctx tools.ToolContext, path, after string, before *string) {
	if tctx.Out == nil || strings.TrimSpace(path) == "" {
		return
	}
	// The recorded edit names each value this call resolved by its reference.
	if before != nil {
		referenced := tctx.Secrets.ReferenceEchoes(*before)
		before = &referenced
	}
	tctx.Out.FileEdit = &tools.FileEditCapture{
		Path:   path,
		Before: before,
		After:  tctx.Secrets.ReferenceEchoes(after),
	}
}

// verifyTextWriteBase compares the destination immediately before replacement.
// An empty hash is the new-file expectation and refuses a path created after
// resolution; a hash refuses any byte change since the validated snapshot,
// which was loaded within the mutation budget.
func verifyTextWriteBase(target fseffect.Target, fullPath, baseSHA256 string) error {
	if baseSHA256 == "" {
		if _, err := target.Lstat(); os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return fmt.Errorf("stat write base: %w", err)
		}
		return &tools.ToolReject{Code: "TEXT_WRITE_CONFLICT", Data: map[string]any{"path": fullPath, "text_base_changed": true}}
	}
	currentFile, err := target.Open()
	if err != nil {
		return &tools.ToolReject{Code: "TEXT_WRITE_CONFLICT", Data: map[string]any{"path": fullPath}}
	}
	defer func() { _ = currentFile.Close() }()
	info, err := currentFile.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > readcaps.MaxMutationBytes {
		return &tools.ToolReject{Code: "TEXT_WRITE_CONFLICT", Data: map[string]any{"path": fullPath}}
	}
	current, err := io.ReadAll(io.LimitReader(currentFile, readcaps.MaxMutationBytes+1))
	if err != nil {
		return &tools.ToolReject{Code: "TEXT_WRITE_CONFLICT", Data: map[string]any{"path": fullPath}}
	}
	if textfile.SHA256(current) != baseSHA256 {
		return &tools.ToolReject{Code: "TEXT_WRITE_CONFLICT", Data: map[string]any{"path": fullPath, "text_base_changed": true}}
	}
	return nil
}

// CommandTool executes commands within the invocation's confinement boundary.
type CommandTool struct {
	DeclaredCommand DeclaredVerifyCommand
	Runner          *hostcmd.Runner
	Boundary        *sandbox.Boundary
	Background      *bgprocess.Registry
	FailureTracker  *CommandFailureTracker
	WriteRootGate   SandboxWriteRootGate
}

func (t *CommandTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	tctx.VerificationCheck = verificationRequested(args)
	if t.DeclaredCommand != nil {
		declared := t.DeclaredCommand(tctx.ActiveRootPath())
		tctx.VerificationCheck = tctx.VerificationCheck || (strings.TrimSpace(declared) != "" && commandsurface.SameCommandLine(canonicalCommandKey(tctx, args), declared))
	}
	if t.Runner == nil {
		return "", fmt.Errorf("command runner not configured")
	}
	if t.FailureTracker == nil {
		t.FailureTracker = NewCommandFailureTracker()
	}
	if toolkit.BoolArg(args, "background", false) {
		return runCommandBackground(ctx, t.Background, t.Runner, t.Boundary, args, tctx, t.confined().sessionWriteRoots(ctx, tctx), "command")
	}
	res, outcome, err := t.confined().run(ctx, args, tctx)
	if err != nil {
		return "", err
	}
	if !outcome.Finished {
		return encodeCommandRunning(tctx, outcome, commandWaitBudget(args))
	}
	stampBoundaryRefusal(tctx, res)
	stampIndexWatch(tctx, outcome.IndexWatch)
	commandVerdict, _ := verdictFor(res)
	stampSourceRun(tctx, res, commandVerdict, outcome)
	var payload any = res
	if outcome.Terminal != nil {
		caption := terminalCaptureCaption(args)
		capture := terminaltool.CaptureFromScreen(ctx, t.Background, tctx, outcome.Terminal.Screen, caption)
		payload = struct {
			*hostcmd.Result
			TerminalCapture terminaltool.SnapshotResult `json:"terminal_capture"`
		}{Result: res, TerminalCapture: capture}
	} else if outcome.SnapshotCapture != nil {
		payload = struct {
			*hostcmd.Result
			SnapshotCapture *SnapshotCaptureResult `json:"snapshot_capture"`
		}{Result: res, SnapshotCapture: outcome.SnapshotCapture}
	}
	out, err := surveyjson.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("command encode: %w", err)
	}
	return string(out), nil
}

// confined binds this tool's wiring to the shared confined-foreground path.
func (t *CommandTool) confined() confinedForeground {
	return confinedForeground{
		Background:     t.Background,
		FailureTracker: t.FailureTracker,
		Runner:         t.Runner,
		Boundary:       t.Boundary,
		WriteRootGate:  t.WriteRootGate,
		ToolName:       "command",
	}
}

// boundaryNetworkMode returns the applied egress label.
func boundaryNetworkMode(b confine.Boundary) string {
	if !b.Applied {
		return ""
	}
	return confine.NetworkLabel(b.Network)
}

// commandConfinementReport builds the process boundary report.
func commandConfinementReport(
	boundary confine.Boundary, posture string, local confine.LocalNetworkGrant,
	packageExecution *packageexec.Execution,
) confine.Report {
	report := confine.ReportOf(boundary)
	report.NetworkPosture = posture
	report = report.WithLocalNetwork(local)
	if packageExecution != nil {
		report = report.WithRemotePackageExecution(packageExecution.AllowedHosts, packageExecution.ApprovedReadPaths)
	}
	return report
}

// commandResultFromOutcome builds a finished foreground result.
func commandResultFromOutcome(
	outcome commandRunOutcome,
	local confine.LocalNetworkGrant,
	packageExecution *packageexec.Execution,
) *hostcmd.Result {
	res := &hostcmd.Result{
		Stages:            outcome.Snapshot.Stages,
		ExitCode:          outcome.Snapshot.ExitCode,
		Tail:              outcome.Snapshot.Tail,
		OK:                outcome.Snapshot.ExitCode == 0 && outcome.Snapshot.Failure == nil && outcome.Snapshot.TerminationReason != bgprocess.TerminationStopped && outcome.Snapshot.TerminationReason != bgprocess.TerminationTimedOut,
		ExecFailure:       outcome.Snapshot.Failure,
		TerminationReason: string(outcome.Snapshot.TerminationReason),
		Network:           outcome.Network,
		Report:            commandConfinementReport(outcome.Boundary, outcome.NetworkPosture, local, packageExecution).WithSandboxRefusals(outcome.Refusals),
		StdinProvided:     outcome.IO.StdinProvided,
		StdinFrom:         outcome.IO.StdinFrom,
		StdoutTo:          outcome.IO.StdoutTo,
		StderrTo:          outcome.IO.StderrTo,
		EnvKeys:           outcome.IO.EnvKeys,
		Cwd:               outcome.Cwd,
		LeftRunning:       outcome.LeftRunning,
	}
	res.Tail = enginepaths.RewriteWorkerBranchPaths(res.Tail) // compilers print the absolute cwd
	if snap := outcome.Snapshot; len(snap.Output) > len(snap.Tail) || snap.OutputEvicted {
		res.Truncated = true
		res.OriginalTailBytes = len(snap.Output)
		res.WireSpillPath = outcome.SpillPath
	}
	if tail, truncated, orig := CapOpaqueTail(res.Tail, 0); truncated {
		res.Tail = tail
		res.Truncated = true
		res.OriginalTailBytes = max(res.OriginalTailBytes, orig)
	}
	return res
}

// captureProcessHandle records a live process on the invocation.
func captureProcessHandle(tctx tools.ToolContext, handle string, running bool) {
	if tctx.Out == nil || strings.TrimSpace(handle) == "" {
		return
	}
	tctx.Out.Process = &api.ToolProcessHandle{Handle: handle, Running: running}
}

// encodeCommandRunning renders a foreground command promoted to a live handle,
// with what the kernel has refused it so far.
func encodeCommandRunning(tctx tools.ToolContext, outcome commandRunOutcome, budget time.Duration) (string, error) {
	captureProcessHandle(tctx, outcome.Handle, true)
	report := commandConfinementReport(
		outcome.Boundary, outcome.NetworkPosture, tools.LocalNetworkGrantOf(tctx), tctx.PackageExecution,
	)
	if outcome.Boundary.Applied {
		stamped := confine.StampRefusal("command", tctx.SessionID, outcome.Boundary, confine.RefusalContext{
			MediatedNetwork:        outcome.Network,
			RemotePackageExecution: report.RemotePackageExecution,
			Running:                true,
			Refusals:               outcome.Refusals,
		})
		report.BoundaryRefusal = string(stamped.Attribution)
		if tctx.Out != nil {
			tctx.Out.Facts = tools.ApplyRefusalFacts(tctx.Out.Facts, stamped)
		}
	}
	out, err := surveyjson.Marshal(hostcmd.CommandRunningResult{
		Running:  true,
		Handle:   outcome.Handle,
		Stages:   outcome.Snapshot.Stages,
		Tail:     outcome.Snapshot.Tail,
		WaitedMs: int(budget.Milliseconds()),
		Network:  outcome.Network,
		Report:   report.WithSandboxRefusals(outcome.Refusals),
	})
	if err != nil {
		return "", fmt.Errorf("command running encode: %w", err)
	}
	return string(out), nil
}
