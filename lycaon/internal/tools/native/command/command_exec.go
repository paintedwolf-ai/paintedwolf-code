package command

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/indexwatch"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// VerifyToolName is the tool identity the deadline and write-scope paths read.
const VerifyToolName = "verify"

// commandCancelCleanupTimeout outlasts the kill fallback so a cancelled command
// settles inline instead of being promoted as still running.
const commandCancelCleanupTimeout = exec.TerminateGrace + exec.PipelineWaitDelay + 3*time.Second

// RunOutcome carries an inline completion or a promoted process handle.
type RunOutcome struct {
	IsCheck          bool
	SourceRevision   string
	SourceRootDigest string
	Finished         bool
	Handle           string
	Snapshot         bgprocess.Snapshot
	// Boundary records the confinement applied to this dispatch.
	Boundary       confine.Boundary
	NetworkPosture string
	Network        []confine.EgressHost
	// LeftRunning counts descendants that outlived this command.
	LeftRunning int
	IO          hostcmd.IOParams
	Terminal    *bgprocess.PTYCaptureResult
	// Cwd is the repo-relative process Dir ("." = root).
	Cwd string
	// SpillPath names the complete screened output when retention succeeded.
	SpillPath string
	// Refusals is what the kernel refused the action: settled when it
	// finished, and what a running result shows otherwise.
	Refusals confine.SandboxRefusals
	// IndexWatch is the Git index captured at spawn; the result's facts own it.
	IndexWatch      indexwatch.Snapshot
	SnapshotCapture *SnapshotCaptureResult
}

// CanonicalToolArgs returns reference-bearing arguments when available.
func CanonicalToolArgs(tctx tools.ToolContext, args map[string]any) map[string]any {
	return canonicalToolArgs(tctx, args)
}

func canonicalToolArgs(tctx tools.ToolContext, args map[string]any) map[string]any {
	if tctx.CanonicalArgs != nil {
		return tctx.CanonicalArgs
	}
	return args
}

// CanonicalCommandKey returns the reference-bearing command identity.
func CanonicalCommandKey(tctx tools.ToolContext, args map[string]any) string {
	return commandsurface.PrimaryCommandLine(canonicalToolArgs(tctx, args), nil)
}

func canonicalCommandKey(tctx tools.ToolContext, args map[string]any) string {
	return CanonicalCommandKey(tctx, args)
}

// commandEgressIdentity supplies the shared subject for posture, leases, and attribution.
func commandEgressIdentity(tctx tools.ToolContext, toolName, commandLine string) confine.EgressCommand {
	identity := confine.EgressCommand{
		SessionID:     tctx.SessionID,
		RootSessionID: tctx.ChatSessionID(),
		ProjectID:     tctx.ProjectID,
		ProjectDir:    tctx.ActiveRootPath(),
		ToolCallID:    tctx.ToolCallID,
		ToolName:      toolName,
		CommandLine:   commandLine,
	}
	if tctx.PackageExecution != nil {
		identity.DeclaredHosts = append([]string(nil), tctx.PackageExecution.AllowedHosts...)
		identity.ReducedPackageExecution = true
	}
	return identity
}

// commandNetworkPosture is the ask-line in force for this dispatch.
func commandNetworkPosture(tctx tools.ToolContext, toolName, commandLine string) string {
	return confine.PostureString(confine.EffectivePosture(commandEgressIdentity(tctx, toolName, commandLine)))
}

// WaitBudget resolves the inline wait budget from wait_ms (clamped to MaxCommandWait),
// falling back to DefaultCommandWait.
func WaitBudget(args map[string]any) time.Duration {
	return commandWaitBudget(args)
}

// commandWaitBudget resolves the inline wait budget from wait_ms (clamped to MaxCommandWait),
// falling back to DefaultCommandWait.
func commandWaitBudget(args map[string]any) time.Duration {
	if v, ok := args["wait_ms"].(float64); ok && v > 0 {
		d := time.Duration(v) * time.Millisecond
		if d > exec.MaxCommandWait {
			return exec.MaxCommandWait
		}
		return d
	}
	return exec.DefaultCommandWait
}

// CommandTimeout resolves the command timeout.
func CommandTimeout(args map[string]any, toolName string) time.Duration {
	return commandTimeout(args, toolName)
}

func commandTimeout(args map[string]any, toolName string) time.Duration {
	if v, ok := args["timeout_ms"].(float64); ok && v > 0 {
		d := time.Duration(v) * time.Millisecond
		// Mirror the schema bounds for callers that bypass schema validation.
		if d < time.Second {
			d = time.Second
		}
		if d > exec.MaxCommandTimeout {
			return exec.MaxCommandTimeout
		}
		return d
	}
	if toolName == VerifyToolName {
		return exec.DefaultVerifyTimeout
	}
	return 0
}

func commandRunID(tctx tools.ToolContext) string {
	if tctx.ParentSessionID != "" {
		return tctx.ParentSessionID
	}
	return tctx.SessionID
}

func allowConcurrent(args map[string]any) bool {
	v, _ := args["allow_concurrent"].(bool)
	return v
}

// runCommandForeground promotes jobs that exceed the foreground wait budget.
func runCommandForeground(
	ctx context.Context,
	registry *bgprocess.Registry,
	ft *CommandFailureTracker,
	runner *hostcmd.Runner,
	boundary *sandbox.Boundary,
	args map[string]any,
	tctx tools.ToolContext,
	budget time.Duration,
	confineJob bool,
	extraWriteRoots []string,
	toolName string,
) (RunOutcome, error) {
	if runner == nil {
		return RunOutcome{}, fmt.Errorf("command runner not configured")
	}
	plan, err := tctx.CommandPlan(args)
	if err != nil {
		return RunOutcome{}, err
	}
	stages := plan.Stages
	ioParams, err := CommandIO(ctx, boundary, tctx, plan, args, toolName)
	if err != nil {
		return RunOutcome{}, err
	}
	cwdArg, _ := args["cwd"].(string)
	cwd, cwdDisplay, err := projectpaths.CommandCwd(ctx, tctx, cwdArg)
	if err != nil {
		return RunOutcome{}, err
	}
	if err := tools.ValidateWorkerBranch(ctx, tctx); err != nil {
		return RunOutcome{}, err
	}
	if tr := rejectPwdEnvMismatch(args, canonicalCommandKey(tctx, args), cwd); tr != nil {
		return RunOutcome{}, tr
	}
	cmdKey := canonicalCommandKey(tctx, args)
	approach := commandFailureApproachKey(canonicalToolArgs(tctx, args))
	if tr := ft.rejectLoop(tctx.SessionID, approach, cmdKey); tr != nil {
		return RunOutcome{}, tr
	}
	// Validate stages before acquiring confinement resources.
	profile := tctx.ProfileID()
	if err := runner.ValidateStages(ctx, stages); err != nil {
		return RunOutcome{}, err
	}
	bound, err := bindCommandForegroundConfine(ctx, tctx, extraWriteRoots, cmdKey, toolName, confineJob)
	if err != nil {
		return RunOutcome{}, err
	}
	confinement := bound.confinement
	directIPApplied := bound.directIPApplied
	if registry == nil {
		bound.close(ctx)
		return RunOutcome{}, fmt.Errorf("background registry not configured")
	}
	req := hostcmd.Request{
		Launch:     agentCommandLaunch(tctx, toolName, confinement),
		ProjectDir: cwd,
		ProfileID:  profile,
		Stages:     stages,
		IOParams:   ioParams,
		PathExtra:  append([]string(nil), tctx.HostResourcePathExtra...),
	}
	spawnFacts := confine.SpawnFacts{Report: CommandConfinementReport(
		confine.BoundaryOf(confinement), commandNetworkPosture(tctx, toolName, cmdKey),
		tools.LocalNetworkGrantOf(tctx), tctx.PackageExecution,
	), Action: bound.lease}
	if bound.lease != nil {
		spawnFacts.Network = bound.lease.ObservedHosts
	}
	var sourceRevision, sourceRootDigest string
	if tctx.VerificationCheck {
		sourceRevision, sourceRootDigest = sourceledger.VerificationState(ctx, tctx.SourceLedger, tools.HostWriteRoot(tctx))
	}
	if err := tctx.Secrets.HandOff(ctx, nil); err != nil {
		return RunOutcome{}, tools.HeldHandOffReject(toolName, err)
	}
	index := watchIndex(tctx, confinement)
	handle, err := registry.StartPipeline(ctx, bgprocess.PipelineSpec{
		IsCheck:        tctx.VerificationCheck,
		SourceRevision: sourceRevision, SourceRootDigest: sourceRootDigest, Cwd: cwdDisplay,
		SessionID: tctx.SessionID, RootSessionID: tctx.ChatSessionID(),
		ProjectID: tctx.ProjectID, Request: req,
		Runner: runner,
		Mode:   bgprocess.JobModeAwaited, OriginTool: toolName,
		ToolCallID: tctx.ToolCallID, RunID: commandRunID(tctx),
		Timeout: commandTimeout(args, toolName), AllowConcurrent: allowConcurrent(args),
		Facts: spawnFacts,
	})
	if err != nil {
		index.Release()
		bound.close(ctx)
		return RunOutcome{}, commandStartError(err)
	}
	if directIPApplied {
		tools.EmitDirectIPLifecycle(tctx, tools.DirectIPLifecycleStarted)
	}
	registry.Lifecycle.WatchIndex(tctx.SessionID, handle, index)
	networkLife := newCommandNetworkLifecycle(tctx, toolName, bound.lease, directIPApplied)
	registry.Lifecycle.OnExit(tctx.SessionID, handle, func() { networkLife.complete(ctx) })

	finished, awaitErr := registry.Lifecycle.Await(ctx, tctx.SessionID, handle, budget)
	if awaitErr != nil {
		return RunOutcome{}, errors.Join(awaitErr, stopAwaitedCommand(ctx, registry, tctx.SessionID, handle))
	}
	snap, err := registry.Snapshot(ctx, tctx.SessionID, handle, bgprocess.DefaultTailBytes)
	if err != nil {
		return RunOutcome{}, errors.Join(err, stopAwaitedCommand(ctx, registry, tctx.SessionID, handle))
	}

	out := RunOutcome{
		IsCheck:        tctx.VerificationCheck,
		SourceRevision: sourceRevision, SourceRootDigest: sourceRootDigest,
		Finished: finished,
		Handle:   handle,
		Snapshot: snap,
		Boundary: confine.BoundaryOf(confinement),
		IO:       ioParams,
		Cwd:      cwdDisplay,
	}
	if confinement != nil {
		out.NetworkPosture = commandNetworkPosture(tctx, toolName, cmdKey)
	}
	if finished {
		out.Network = networkLife.complete(ctx)
		out.Refusals = bound.lease.SettledRefusals(ctx)
		out.LeftRunning = networkLife.leftBehind()
		out.SpillPath = spillCommandOutput(tctx, snap)
		ft.record(tctx.SessionID, approach, out.Snapshot.ExitCode == 0)
		out.IndexWatch = registry.Lifecycle.TakeIndexWatch(tctx.SessionID, handle)
		registry.Lifecycle.Discard(tctx.SessionID, handle)
		recordContainerLaunch(tctx, snap)
	} else {
		// The running result shows these; only later refusals send a notice.
		out.Refusals = bound.lease.Refusals()
		registry.Output.NoteRefusalsShown(tctx.SessionID, handle, len(out.Refusals.Refusals))
		// Promoted commands retain egress attribution after the tool call.
		if err := registry.Output.Promote(ctx, tctx.SessionID, handle); err != nil {
			return RunOutcome{}, errors.Join(err, stopAwaitedCommand(ctx, registry, tctx.SessionID, handle))
		}
	}
	tools.CaptureExternalAccess(tctx, out.Network, directIPApplied)
	return out, nil
}

func recordContainerLaunch(tctx tools.ToolContext, snap bgprocess.Snapshot) {
	if tctx.ContainerRecorder == nil || len(tctx.SocketGrants) == 0 {
		return
	}
	engineSocket := ""
	for _, g := range tctx.SocketGrants {
		p := g.ResolvedPath
		if p == "" {
			p = g.ApprovedPath
		}
		if p != "" {
			engineSocket = p
			break
		}
	}
	if engineSocket == "" {
		return
	}
	outStr := strings.TrimSpace(string(snap.Tail))
	if len(outStr) != 64 {
		outStr = strings.TrimSpace(string(snap.Output))
	}
	if len(outStr) == 64 && isHex64(outStr) {
		rootSession := tctx.RootSessionID
		if rootSession == "" {
			rootSession = tctx.ParentSessionID
		}
		if rootSession == "" {
			rootSession = tctx.SessionID
		}
		tctx.ContainerRecorder.RecordSessionContainer(rootSession, outStr, engineSocket)
	}
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// spillCommandOutput persists the whole screened output when the tail dropped
// some of it; the result names the file, readable by line.
func spillCommandOutput(tctx tools.ToolContext, snap bgprocess.Snapshot) string {
	if !snap.OutputScreened || len(snap.Output) <= len(snap.Tail) {
		return ""
	}
	host := strings.TrimSpace(tctx.HostDataDir)
	if host == "" {
		return ""
	}
	return tooloutput.SpillWholeRaw(host, tooloutput.Screened(snap.Output), tctx.MaxToolSpillBytes).SpillPath
}

func stopAwaitedCommand(ctx context.Context, registry *bgprocess.Registry, sessionID, handle string) error {
	if registry == nil {
		return nil
	}
	_, stopErr := registry.Lifecycle.Stop(sessionID, handle)
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commandCancelCleanupTimeout)
	defer cancel()
	finished, waitErr := registry.Lifecycle.Await(cleanupCtx, sessionID, handle, 0)
	if waitErr == nil && finished {
		registry.Lifecycle.Discard(sessionID, handle)
		return stopErr
	}
	// Promote a process that outlives cancellation cleanup.
	promoteErr := registry.Output.Promote(cleanupCtx, sessionID, handle)
	return errors.Join(stopErr, waitErr, promoteErr)
}

// commandForegroundConfine manages one command's confinement resources.
type commandForegroundConfine struct {
	confinement     *confine.Confinement
	lease           *confine.ActionLease
	directIPApplied bool
}

func (b commandForegroundConfine) close(ctx context.Context) []confine.EgressHost {
	if b.lease == nil {
		return nil
	}
	return b.lease.Close(ctx)
}

func bindCommandForegroundConfine(
	ctx context.Context,
	tctx tools.ToolContext,
	extraWriteRoots []string,
	cmdKey string,
	toolName string,
	confineJob bool,
) (commandForegroundConfine, error) {
	bound := commandForegroundConfine{}
	if confineJob {
		// One confinement result feeds execution and approval facts.
		confReq, tr := tools.ConfineRequestForSpawn(ctx, tctx, extraWriteRoots)
		if tr != nil {
			return commandForegroundConfine{}, tr
		}
		resolved, applied := confine.DefaultConfinement(confReq)
		bound.confinement = resolved
		if err := confine.RequireApplied(applied); err != nil {
			return commandForegroundConfine{}, err
		}
		confine.LogApplied("command", tctx.SessionID, bound.confinement)
	}
	bound.directIPApplied = tctx.DirectIPRequested &&
		bound.confinement != nil &&
		bound.confinement.Network == confine.NetworkDirectIP
	// One lease keeps both descendant accountability and egress attribution
	// alive while the process runs.
	lease, err := confine.BindAction(bound.confinement, commandEgressIdentity(tctx, toolName, cmdKey))
	if err != nil {
		return commandForegroundConfine{}, err
	}
	bound.lease = lease
	return bound, nil
}

// CommandFailureTracker records repeated failing confined invocations per session.
type CommandFailureTracker struct {
	mu       sync.Mutex
	failures map[string]commandFailureState
}

type commandFailureState struct {
	LastApproach string
	Count        int
}

// NewCommandFailureTracker constructs a shared failure tracker for command/verify tools.
func NewCommandFailureTracker() *CommandFailureTracker {
	return &CommandFailureTracker{failures: make(map[string]commandFailureState)}
}

// commandFailureApproachKey includes capability requests and proxy settings
// so a changed boundary counts as a new attempt.
func commandFailureApproachKey(args map[string]any) string {
	cwd, _ := args["cwd"].(string)
	socks, _ := args["socks_proxy"].(bool)
	payload, err := surveyjson.Marshal(struct {
		Command    string `json:"command"`
		Cwd        string `json:"cwd,omitempty"`
		SocksProxy bool   `json:"socks_proxy,omitempty"`
		Capability any    `json:"capability_request,omitempty"`
	}{
		Command:    commandsurface.PrimaryCommandLine(args, nil),
		Cwd:        strings.TrimSpace(cwd),
		SocksProxy: socks,
		Capability: args["capability_request"],
	})
	if err != nil {
		return commandsurface.PrimaryCommandLine(args, nil)
	}
	return string(payload)
}

// rejectLoop blocks a command/verify invocation that has already failed with
// the same approach 3 or more times in a row.
func (ft *CommandFailureTracker) rejectLoop(sessionID, approach, command string) *tools.ToolReject {
	if ft == nil || approach == "" {
		return nil
	}
	ft.mu.Lock()
	defer ft.mu.Unlock()
	st := ft.failures[sessionID]
	if approach != st.LastApproach {
		return nil
	}
	if st.Count < 3 {
		return nil
	}
	return &tools.ToolReject{
		Code: "COMMAND_FAILURE_LOOP",
		Data: map[string]any{
			"command": command,
			"count":   st.Count,
		},
	}
}

func (ft *CommandFailureTracker) record(sessionID, approach string, ok bool) {
	if ft == nil {
		return
	}
	ft.mu.Lock()
	defer ft.mu.Unlock()
	st := ft.failures[sessionID]
	if ok {
		st.LastApproach = ""
		st.Count = 0
	} else if approach == st.LastApproach {
		st.Count++
	} else {
		st.LastApproach = approach
		st.Count = 1
	}
	ft.failures[sessionID] = st
}

// rejectPwdEnvMismatch rejects conflicting working-directory inputs.
func rejectPwdEnvMismatch(args map[string]any, canonicalCommand, cwd string) *tools.ToolReject {
	env, ok := args["env"].(map[string]any)
	if !ok {
		return nil
	}
	v, ok := env["PWD"]
	if !ok {
		return nil
	}
	pwd, ok := v.(string)
	if !ok || pwd == "" {
		return nil
	}
	if !filepath.IsAbs(pwd) {
		pwd = filepath.Join(cwd, pwd)
	}
	pwd = filepath.Clean(pwd)
	if pwd == filepath.Clean(cwd) {
		return nil
	}
	return &tools.ToolReject{
		Code: "COMMAND_PWD_NOT_CWD",
		Data: map[string]any{
			"command": canonicalCommand,
			"cwd":     enginepaths.RewriteWorkerBranchPaths(cwd),
			"pwd":     enginepaths.RewriteWorkerBranchPaths(pwd),
		},
	}
}
