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
	"github.com/lycaon/lycaon/internal/pythoncatalog"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/hostenv"
)

// portablePythonRunner holds an optional live portablepython.Service for the
// direct `python` tool handler.
type portablePythonRunner struct {
	mu      sync.RWMutex
	service *portablepython.Service
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
	if p != nil {
		p.mu.RLock()
		svc = p.service
		p.mu.RUnlock()
	}
	if svc == nil {
		return "", errors.New("portable python runtime is not configured")
	}

	var roots []pythondocuments.HostRootConfig
	if rootPath := tc.ActiveRootPath(); rootPath != "" {
		rootID := tc.Source.ActiveRootID
		if rootID == "" {
			rootID = "default"
		}
		roots = append(roots, pythondocuments.HostRootConfig{
			ID:       rootID,
			Branch:   string(tc.Source.ProjectSourceBranch),
			Dir:      rootPath,
			ReadOnly: tc.Source.ReadOnly || tc.Source.AskMode,
		})
	}

	var mutationDoor pythondocuments.MutationDoor
	if len(roots) > 0 {
		mutationDoor = native.NewPythonMutationDoor(tc, roots)
	}

	resp, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
		RawArgs:      args,
		CallID:       tc.Identity.ToolCallID,
		Actor:        tc.Identity.Agent,
		View:         string(tc.Source.SourceWorkspaceKind),
		Surface:      tc.Turn.TurnSurfaceID,
		ReadOnly:     tc.Source.ReadOnly || tc.Source.AskMode,
		Roots:        roots,
		MutationDoor: mutationDoor,
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
	pyHandler := &portablePythonRunner{service: pythonSvc}
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
