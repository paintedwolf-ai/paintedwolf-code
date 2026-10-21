package toolhost

import (
	"context"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/hostprocess"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// commandProgramEntry routes the command tool to the v1 program entry. It
// flips with the statement evaluator; the legacy argv path is then removed.
const commandProgramEntry = false

// pathBuiltins binds the path utilities to the invocation's existing path
// authority. pwd is a native owner elsewhere and cd changes evaluator state,
// so neither is offered here.
func pathBuiltins(boundary *sandbox.Boundary) func(context.Context, tools.ToolContext) (map[string]commandprogram.OperationDispatcher, error) {
	return func(ctx context.Context, tc tools.ToolContext) (map[string]commandprogram.OperationDispatcher, error) {
		paths, err := projectpaths.NewProgramPaths(ctx, boundary, tc, "")
		if err != nil {
			return nil, err
		}
		return map[string]commandprogram.OperationDispatcher{"basename": paths, "dirname": paths, "realpath": paths, "readlink": paths, "native_path": paths}, nil
	}
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
	service, err := hostprocess.New()
	if err != nil {
		return err
	}
	processTools := &native.ProcessTools{Service: service}
	if err := reg.Register("process_list", processTools.List); err != nil {
		return err
	}
	if err := reg.Register("process_signal", processTools.Signal); err != nil {
		return err
	}
	ft := command.NewCommandFailureTracker()
	programs := &command.ProgramRuntime{Registry: reg, Supervisor: &command.ProgramSupervisor{}, Builtins: pathBuiltins(boundary)}
	*commandTool = &command.CommandTool{Runner: runner, Boundary: boundary, FailureTracker: ft, Programs: programs}
	*verifyTool = &native.VerifyTool{Runner: runner, Boundary: boundary, FailureTracker: ft}
	*commandOutputTool = &command.CommandOutputTool{}
	*commandStopTool = &command.CommandStopTool{}
	handler := (*commandTool).Run
	if commandProgramEntry {
		handler = programs.Run
	}
	if err := reg.Register("command", handler); err != nil {
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
