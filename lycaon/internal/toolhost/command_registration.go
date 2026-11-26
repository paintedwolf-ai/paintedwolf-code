package toolhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/hostos"
	"github.com/lycaon/lycaon/internal/hostprocess"
	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/pythonadmission"
	"github.com/lycaon/lycaon/internal/pythoncatalog"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/hostenv"
)

// portablePythonRunner holds an optional live portablepython.Service for the
// direct `python` tool handler.
type portablePythonRunner struct {
	mu       sync.RWMutex
	service  *portablepython.Service
	boundary *sandbox.Boundary
}

func (p *portablePythonRunner) SetService(svc *portablepython.Service) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.service = svc
	p.mu.Unlock()
}

func (p *portablePythonRunner) Service() *portablepython.Service {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.service
}

func (p *portablePythonRunner) Run(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
	if _, err := pythoncatalog.ValidateDirectToolArgs(args); err != nil {
		return "", err
	}
	svc, boundary := p.snapshot()
	if svc == nil {
		return "", errors.New("portable python runtime is not configured")
	}

	req := buildPortablePythonRequest(ctx, boundary, tc, "python")
	req.RawArgs = args
	resp, err := svc.ExecuteTool(ctx, req)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(resp)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// ExecuteCommand implements command.PythonRunner through the exact same
// trusted invocation builder as direct `python`, preserving the actual surface,
// session/call identity, profile command_effects, live editor bridge, host
// mutation door, and egress/approval policy while recording "command" as the
// initiating tool separately from Surface.
func (p *portablePythonRunner) ExecuteCommand(ctx context.Context, cmdReq command.PythonExecutionRequest) (command.PythonExecutionResponse, error) {
	svc, boundary := p.snapshot()
	if svc == nil {
		return command.PythonExecutionResponse{}, errors.New("portable python runtime is not configured")
	}

	req := buildPortablePythonRequest(ctx, boundary, cmdReq.ToolCtx, "command")
	req.Mode = cmdReq.Mode
	req.Code = cmdReq.Code
	req.Script = cmdReq.Script
	req.Module = cmdReq.Module
	req.CLI = cmdReq.CLI
	req.CWD = cmdReq.CWD
	req.Args = cmdReq.Args
	req.Env = cmdReq.Env
	req.Stdin = cmdReq.Stdin
	if cmdReq.ReadOnly {
		req.ReadOnly = true
		if req.AllowedEffects != nil {
			narrowed := *req.AllowedEffects
			narrowed.AllowMutation = false
			req.AllowedEffects = &narrowed
		}
	}
	if len(req.Roots) == 0 && cmdReq.RootPath != "" {
		rootID := cmdReq.RootID
		if rootID == "" {
			rootID = "default"
		}
		req.Roots = []pythondocuments.HostRootConfig{{
			ID:       rootID,
			Dir:      cmdReq.RootPath,
			ReadOnly: req.ReadOnly,
		}}
		req.RootSpecs = []pythondocuments.RootSpec{{
			ID:       rootID,
			Label:    rootID,
			Default:  true,
			ReadOnly: req.ReadOnly,
		}}
		req.MutationDoor = native.NewPythonMutationDoor(cmdReq.ToolCtx, req.Roots)
	}

	resp, err := svc.ExecuteTool(ctx, req)
	if err != nil {
		return command.PythonExecutionResponse{}, err
	}
	out := command.PythonExecutionResponse{
		ExitCode: resp.ExitCode,
		Stdout:   resp.Stdout,
		Stderr:   resp.Stderr,
	}
	if resp.Stop != nil {
		out.StopCode = string(resp.Stop.Code)
		out.StopMsg = resp.Stop.Reason
	}
	return out, nil
}

func (p *portablePythonRunner) snapshot() (*portablepython.Service, *sandbox.Boundary) {
	if p == nil {
		return nil, nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.service, p.boundary
}

// buildPortablePythonRequest is the single common trusted invocation builder
// shared by direct `python` and managed `command` Portable Python entries.
func buildPortablePythonRequest(ctx context.Context, boundary *sandbox.Boundary, tc tools.ToolContext, initiatingTool string) portablepython.ToolRequest {
	allowedEffects, readOnly := resolvePythonAllowedEffects(ctx, boundary, tc)
	roots, rootSpecs := buildPythonRoots(tc, readOnly)

	var mutationDoor pythondocuments.MutationDoor
	if len(roots) > 0 {
		mutationDoor = native.NewPythonMutationDoor(tc, roots)
	}
	editorBridge := newEditorDocumentsBridge(tc)

	return portablepython.ToolRequest{
		CallID:         tc.Identity.ToolCallID,
		SessionID:      tc.Identity.SessionID,
		Actor:          tc.Identity.Agent,
		View:           string(tc.Source.SourceWorkspaceKind),
		Surface:        tc.Turn.TurnSurfaceID,
		InitiatingTool: initiatingTool,
		ReadOnly:       readOnly,
		AllowedEffects: allowedEffects,
		Policy:         buildPythonSessionPolicy(tc),
		Roots:          roots,
		RootSpecs:      rootSpecs,
		Editor:         editorBridge,
		MutationDoor:   mutationDoor,
	}
}

// buildPythonSessionPolicy wires Painted Wolf's session egress destination
// policy and approval broker (confine.DecideAttributedHTTPRequest) into
// Portable Python's OperationObserver. Sensitive effects fail closed whenever
// session identity or destination approval is missing.
func buildPythonSessionPolicy(tc tools.ToolContext) pythonadmission.PolicyEvaluator {
	return func(ctx context.Context, op pythonruntime.ReachedOperation) pythonadmission.PolicyDecision {
		switch op.Family {
		case pythonruntime.EffectHTTP:
			host := strings.TrimSpace(op.Target.RootID)
			if host == "" || strings.TrimSpace(tc.Identity.SessionID) == "" {
				return pythonadmission.PolicyDecision{
					Kind:      pythonruntime.DecisionStop,
					Condition: pythonadmission.ConditionEffectDenied,
					Reason:    "outbound HTTP blocked: missing session identity or destination host",
				}
			}
			method := ""
			if m, ok := op.Args["method"].(string); ok {
				method = m
			}
			cmd := confine.EgressCommand{
				SessionID:     tc.Identity.SessionID,
				RootSessionID: tc.Identity.RootSessionID,
				ProjectID:     tc.Identity.ProjectID,
				ProjectDir:    tc.ActiveRootPath(),
				ToolCallID:    tc.Identity.ToolCallID,
				ToolName:      "python",
				DeclaredHosts: []string{host},
			}
			if !confine.DecideAttributedHTTPRequest(ctx, cmd, host, method, op.Target.RelPath) {
				return pythonadmission.PolicyDecision{
					Kind:      pythonruntime.DecisionStop,
					Condition: pythonadmission.ConditionEffectDenied,
					Reason:    fmt.Sprintf("outbound HTTP to %s denied by session egress policy", host),
				}
			}
			return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionAllow}

		case pythonruntime.EffectPreviewListener:
			if strings.TrimSpace(tc.Identity.SessionID) == "" || tc.Source.AskMode {
				return pythonadmission.PolicyDecision{
					Kind:      pythonruntime.DecisionStop,
					Condition: pythonadmission.ConditionEffectDenied,
					Reason:    "preview listener blocked: requires active non-ask session",
				}
			}
			return pythonadmission.PolicyDecision{
				Kind:   pythonruntime.DecisionAllow,
				Reason: "supervised static preview listener permitted for session",
			}

		case pythonruntime.EffectSubprocess:
			return pythonadmission.PolicyDecision{
				Kind:      pythonruntime.DecisionStop,
				Condition: pythonadmission.ConditionEffectDenied,
				Reason:    fmt.Sprintf("%s not permitted by default python session policy", op.Family),
			}

		default:
			return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionAllow}
		}
	}
}

func mustCommandEffects(classes ...toolcontract.CommandEffectClass) toolcontract.CommandEffects {
	effects, err := toolcontract.NewCommandEffects(classes...)
	if err != nil {
		panic(err)
	}
	return effects
}

var (
	pyEffectReadOnly     = mustCommandEffects(toolcontract.CommandReadOnly)
	pyEffectMutation     = mustCommandEffects(toolcontract.CommandMutation)
	pyEffectExternalExec = mustCommandEffects(toolcontract.CommandExternalExec)
	pyEffectNetwork      = mustCommandEffects(toolcontract.CommandNetworkEgress)
	pyEffectAll          = mustCommandEffects(
		toolcontract.CommandReadOnly,
		toolcontract.CommandMutation,
		toolcontract.CommandExternalExec,
		toolcontract.CommandNetworkEgress,
	)
)

// resolvePythonAllowedEffects computes the effective command_effects posture
// from the active profile (via sandbox.Boundary) and turn surface (via
// surfacecatalog.RestrictCommandEffects), combined with tc.Source.ReadOnly and
// tc.Source.AskMode. Always fails closed when an effect is absent.
func resolvePythonAllowedEffects(ctx context.Context, boundary *sandbox.Boundary, tc tools.ToolContext) (*pythonadmission.AllowedEffects, bool) {
	hasProfile := boundary != nil && tc.Identity.Agent != ""
	hasSurface := tc.Turn.TurnSurfaceID != ""
	hasProfileOrSurface := hasProfile || hasSurface
	var allowed toolcontract.CommandEffects
	if hasProfile {
		if profileEffects, err := boundary.CommandEffects(ctx, tc.Identity.Agent); err == nil {
			allowed = profileEffects
		} else {
			// Fail closed: an unresolved required profile MUST NEVER widen
			// effective permissions to pyEffectAll even when a surface is present.
			allowed = pyEffectReadOnly
		}
	} else if hasSurface {
		// No profile configured, but a surface was explicitly named: start
		// from full effects and let RestrictCommandEffects intersect.
		allowed = pyEffectAll
	}
	if hasSurface {
		if restricted, err := surfacecatalog.RestrictCommandEffects(allowed, tc.Turn.TurnSurfaceID); err == nil {
			allowed = restricted
		} else {
			// Unknown or invalid surface fails closed to read-only.
			allowed = pyEffectReadOnly
		}
	}

	var ae pythonadmission.AllowedEffects
	if hasProfileOrSurface {
		ae.AllowMutation = allowed.Allows(pyEffectMutation)
		ae.AllowNetworkEgress = allowed.Allows(pyEffectNetwork)
		ae.AllowExternalExec = allowed.Allows(pyEffectExternalExec)
	} else {
		ae.AllowMutation = true
		ae.AllowNetworkEgress = false
		ae.AllowExternalExec = false
	}

	if tc.Source.ReadOnly {
		ae.AllowMutation = false
	}
	if tc.Source.AskMode {
		ae.AllowMutation = false
		ae.AllowNetworkEgress = false
		ae.AllowExternalExec = false
	}

	readOnly := tc.Source.ReadOnly || tc.Source.AskMode || !ae.AllowMutation
	return &ae, readOnly
}

// buildPythonRoots mounts all attached workspace roots from tc.Source.Roots
// (preserving labels for @label/... addressing and active default root), or
// falls back to tc.ActiveRootPath() when tc.Source.Roots is empty.
func buildPythonRoots(tc tools.ToolContext, readOnly bool) ([]pythondocuments.HostRootConfig, []pythondocuments.RootSpec) {
	branch := string(tc.Source.ProjectSourceBranch)
	if len(tc.Source.Roots) > 0 {
		roots := make([]pythondocuments.HostRootConfig, 0, len(tc.Source.Roots))
		specs := make([]pythondocuments.RootSpec, 0, len(tc.Source.Roots))
		activeID := tc.Source.ActiveRootID
		if activeID == "" {
			activeID = tc.Source.Roots[0].ID
		}
		for i, r := range tc.Source.Roots {
			if r.Path == "" {
				continue
			}
			id := r.ID
			if id == "" {
				id = "default"
			}
			label := r.Label
			if label == "" {
				label = id
			}
			isDefault := (id == activeID) || (activeID == "" && i == 0)
			roots = append(roots, pythondocuments.HostRootConfig{
				ID:       id,
				Branch:   branch,
				Dir:      r.Path,
				ReadOnly: readOnly,
			})
			specs = append(specs, pythondocuments.RootSpec{
				ID:       id,
				Label:    label,
				Branch:   branch,
				Default:  isDefault,
				ReadOnly: readOnly,
			})
		}
		if len(roots) > 0 {
			return roots, specs
		}
	}

	if rootPath := tc.ActiveRootPath(); rootPath != "" {
		rootID := tc.Source.ActiveRootID
		if rootID == "" {
			rootID = "default"
		}
		roots := []pythondocuments.HostRootConfig{{
			ID:       rootID,
			Branch:   branch,
			Dir:      rootPath,
			ReadOnly: readOnly,
		}}
		specs := []pythondocuments.RootSpec{{
			ID:       rootID,
			Label:    rootID,
			Branch:   branch,
			Default:  true,
			ReadOnly: readOnly,
		}}
		return roots, specs
	}
	return nil, nil
}

// formTools are callable tools that run through their native command form.
var formTools = []struct {
	name string
	argv func(map[string]any) []string
}{
	{"summarize", toolreach.SummarizeArgv},
	{"survey_repo", toolreach.SurveyRepoArgv},
	{"code_rewrite", toolreach.CodeRewriteArgv},
}

func registerCommandTools(
	reg *tools.DefaultRegistry,
	runner *hostcmd.Runner,
	boundary *sandbox.Boundary,
	commandTool **command.CommandTool,
	verifyTool **native.VerifyTool,
	commandOutputTool **command.CommandOutputTool,
	commandStopTool **command.CommandStopTool,
) error {
	var pyRunner *portablePythonRunner
	return registerCommandToolsWithPython(reg, runner, boundary, nil, commandTool, verifyTool, commandOutputTool, commandStopTool, &pyRunner)
}

func registerCommandToolsWithPython(
	reg *tools.DefaultRegistry,
	runner *hostcmd.Runner,
	boundary *sandbox.Boundary,
	pythonSvc *portablepython.Service,
	commandTool **command.CommandTool,
	verifyTool **native.VerifyTool,
	commandOutputTool **command.CommandOutputTool,
	commandStopTool **command.CommandStopTool,
	pythonRunner **portablePythonRunner,
) error {
	service, err := hostprocess.New()
	if err != nil {
		return err
	}
	ft := command.NewCommandFailureTracker()
	hostUtilities, err := hostenv.NewOwners(hostos.Host{}, time.Now, boundary)

	if err != nil {
		return err
	}
	pyHandler := &portablePythonRunner{service: pythonSvc, boundary: boundary}
	if pythonRunner != nil {
		*pythonRunner = pyHandler
	}
	programs := &command.ProgramRuntime{
		Registry:       reg,
		Boundary:       boundary,
		Supervisor:     &command.ProgramSupervisor{},
		Evaluate:       commandprogram.Evaluate,
		Ports:          service,
		FailureTracker: ft,
		PortablePython: pyHandler,
		DirectPythonOffered: func(tc tools.ToolContext) bool {
			if tc.Turn.TurnOfferedToolNames != nil {
				return slices.Contains(tc.Turn.TurnOfferedToolNames, "python")
			}
			if boundary != nil {
				if profileID := strings.TrimSpace(tc.ProfileID()); profileID != "" {
					return boundary.ProfileAllowsTool(profileID, "python")
				}
			}
			return false
		},
		Builtins: func(tc tools.ToolContext, nested commandprogram.NestedCommand, bound command.NativeBindings) map[string]commandprogram.OperationDispatcher {
			return hostUtilities.Builtins(tc, nested, bound)
		},
	}
	*commandTool = &command.CommandTool{Runner: runner, Boundary: boundary, FailureTracker: ft, Programs: programs}
	*verifyTool = &native.VerifyTool{Programs: programs}
	*commandOutputTool = &command.CommandOutputTool{}
	*commandStopTool = &command.CommandStopTool{}
	if err := reg.Register("command", (*commandTool).Run); err != nil {
		return err
	}
	if err := reg.Register("python", pyHandler.Run); err != nil {
		return err
	}
	if err := reg.Register("verify", (*verifyTool).Run); err != nil {
		return err
	}
	if err := reg.Register("command_output", (*commandOutputTool).Run); err != nil {
		return err
	}
	return reg.Register("command_stop", (*commandStopTool).Run)
}
