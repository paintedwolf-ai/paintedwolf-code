package toolhost

import (
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/hostprocess"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

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
	*commandTool = &command.CommandTool{Runner: runner, Boundary: boundary, FailureTracker: ft}
	*verifyTool = &native.VerifyTool{Runner: runner, Boundary: boundary, FailureTracker: ft}
	*commandOutputTool = &command.CommandOutputTool{}
	*commandStopTool = &command.CommandStopTool{}
	if err := reg.Register("command", (*commandTool).Run); err != nil {
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
