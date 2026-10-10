package command

import (
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hostcmd"
)

// rejectCommandExecDenied reports a confine helper exec refusal as a reject.
func rejectCommandExecDenied(res *hostcmd.Result, args map[string]any) error {
	if res == nil || !confine.ReportsExecDenied(res.ExitCode, res.Tail) {
		return nil
	}
	return commandExecDeniedReject(commandArgv0(args))
}

func commandExecDeniedReject(path string) error {
	path = strings.TrimSpace(path)
	data := map[string]any{}
	if path != "" {
		data["path"] = path
	}
	return &toolrejection.ToolReject{Code: "COMMAND_EXEC_DENIED", Data: data}
}

func rejectStartCommandExecDenied(err error) error {
	var denied *confine.ExecDeniedError
	if !errors.As(err, &denied) {
		return nil
	}
	return commandExecDeniedReject(denied.Path)
}
