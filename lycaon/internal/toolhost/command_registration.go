package toolhost

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/hostos"
	"github.com/lycaon/lycaon/internal/hostprocess"
	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/pythonadmission"
	"github.com/lycaon/lycaon/internal/pythoncatalog"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/surfacecatalog"
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

func (p *portablePythonRunner) Run(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
	if _, err := pythoncatalog.ValidateDirectToolArgs(args); err != nil {
		return "", err
	}
	var svc *portablepython.Service
	var boundary *sandbox.Boundary
	if p != nil {
		p.mu.RLock()
		svc = p.service
		boundary = p.boundary
		p.mu.RUnlock()
	}
	if svc == nil {
		return "", errors.New("portable python runtime is not configured")
	}

	allowedEffects, readOnly := resolvePythonAllowedEffects(ctx, boundary, tc)
	roots, rootSpecs := buildPythonRoots(tc, readOnly)

	var mutationDoor pythondocuments.MutationDoor
	if len(roots) > 0 {
		mutationDoor = native.NewPythonMutationDoor(tc, roots)
	}
	editorBridge := newEditorDocumentsBridge(tc)

	resp, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
		RawArgs:        args,
		CallID:         tc.Identity.ToolCallID,
		SessionID:      tc.Identity.SessionID,
		Actor:          tc.Identity.Agent,
		View:           string(tc.Source.SourceWorkspaceKind),
		Surface:        tc.Turn.TurnSurfaceID,
		ReadOnly:       readOnly,
		AllowedEffects: allowedEffects,
		Roots:          roots,
		RootSpecs:      rootSpecs,
		Editor:         editorBridge,
		MutationDoor:   mutationDoor,
	})
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(resp)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// resolvePythonAllowedEffects computes the effective command_effects posture
// from the active profile (via sandbox.Boundary) and turn surface (via
// surfacecatalog.RestrictCommandEffects), combined with tc.Source.ReadOnly and
// tc.Source.AskMode. Always fails closed when an effect is absent.
func resolvePythonAllowedEffects(ctx context.Context, boundary *sandbox.Boundary, tc tools.ToolContext) (*pythonadmission.AllowedEffects, bool) {
	hasProfileOrSurface := boundary != nil || tc.Turn.TurnSurfaceID != ""
	var allowed toolcontract.CommandEffects
	if boundary != nil {
		if profileEffects, err := boundary.CommandEffects(ctx, tc.Turn.ProfileID); err == nil {
			allowed = profileEffects
		}
	} else if tc.Turn.TurnSurfaceID != "" {
		// No boundary configured, but a surface was explicitly named: start
		// from full effects and let RestrictCommandEffects intersect.
		allowed = toolcontract.NewCommandEffects(
			toolcontract.CommandReadOnly,
			toolcontract.CommandMutation,
			toolcontract.CommandExternalExec,
			toolcontract.CommandNetworkEgress,
		)
	}
	if tc.Turn.TurnSurfaceID != "" {
		if restricted, err := surfacecatalog.RestrictCommandEffects(allowed, tc.Turn.TurnSurfaceID); err == nil {
			allowed = restricted
		} else {
			// Unknown or invalid surface fails closed to read-only.
			allowed = toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
		}
	}

	var ae pythonadmission.AllowedEffects
	if hasProfileOrSurface {
		ae.AllowMutation = allowed.Allows(toolcontract.NewCommandEffects(toolcontract.CommandMutation))
		ae.AllowNetworkEgress = allowed.Allows(toolcontract.NewCommandEffects(toolcontract.CommandNetworkEgress))
		ae.AllowExternalExec = allowed.Allows(toolcontract.NewCommandEffects(toolcontract.CommandExternalExec))
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
	programs := &command.ProgramRuntime{Registry: reg, Boundary: boundary, Supervisor: &command.ProgramSupervisor{}, Evaluate: commandprogram.Evaluate, Ports: service, FailureTracker: ft,
		Builtins: func(tc tools.ToolContext, nested commandprogram.NestedCommand, bound command.NativeBindings) map[string]commandprogram.OperationDispatcher {
			return hostUtilities.Builtins(tc, nested, bound)
		}}
	*commandTool = &command.CommandTool{Runner: runner, Boundary: boundary, FailureTracker: ft, Programs: programs}
	*verifyTool = &native.VerifyTool{Programs: programs}
	*commandOutputTool = &command.CommandOutputTool{}
	*commandStopTool = &command.CommandStopTool{}
	pyHandler := &portablePythonRunner{service: pythonSvc, boundary: boundary}
	if pythonRunner != nil {
		*pythonRunner = pyHandler
	}
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
