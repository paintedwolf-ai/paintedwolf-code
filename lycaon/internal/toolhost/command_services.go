package toolhost

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolexecution"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

// CommandServices owns native command configuration and background lifecycle.
type CommandServices struct {
	bgRegistry        *bgprocess.Registry
	commandTool       *native.CommandTool
	verifyTool        *native.VerifyTool
	commandOutputTool *native.CommandOutputTool
	commandStopTool   *native.CommandStopTool
	reviews           *toolexecution.Approvals
	paths             *toolexecution.Boundary
}

func (r *CommandServices) SetBackgroundRegistry(reg *bgprocess.Registry) {
	if r == nil {
		return
	}
	r.bgRegistry = reg
	if r.commandTool != nil {
		r.commandTool.Background = reg
	}
	if r.verifyTool != nil {
		r.verifyTool.Background = reg
	}
	if r.commandOutputTool != nil {
		r.commandOutputTool.Registry = reg
	}
	if r.commandStopTool != nil {
		r.commandStopTool.Registry = reg
	}
	if r.reviews != nil {
		r.reviews.SetBackgroundCommandResolver(r.backgroundCommandLine)
	}
}

func (r *CommandServices) SetSandboxWriteRootGate(writeRoot native.SandboxWriteRootGate) {
	if r == nil {
		return
	}
	if r.commandTool != nil {
		r.commandTool.WriteRootGate = writeRoot
	}
	if r.verifyTool != nil {
		r.verifyTool.WriteRootGate = writeRoot
	}
	if r.paths != nil {
		r.paths.SetSessionWriteRootOverlay(writeRoot.SessionWriteRoots)
		r.paths.SetWriteRootPreflight(func(ctx context.Context, tool string, args map[string]any, tc tools.ToolContext, root string) (bool, bool, string, error) {
			result, err := writeRoot.Authorize(ctx, native.SandboxWriteRootAsk{
				SessionID: tc.SessionID, ParentSessionID: tc.ParentSessionID,
				ProjectID: tc.ProjectID, ToolCallID: tc.ToolCallID, ProjectDir: tc.ActiveRootPath(),
				ToolName: tool, Command: commandsurface.PrimaryCommandLine(args, nil), ProposedWriteRoot: root,
				SessionScratchRoot: tc.SessionScratchDir,
			})
			return result.Authorized, result.Denied, result.UserGuidance, err
		})
		r.paths.SetSessionReadPathOverlay(writeRoot.SessionReadPaths)
		r.paths.SetReadPathPreflight(func(ctx context.Context, tool string, args map[string]any, tc tools.ToolContext, path string) (bool, bool, string, error) {
			result, err := writeRoot.AuthorizeRead(ctx, native.SandboxReadPathAsk{
				SessionID: tc.SessionID, ParentSessionID: tc.ParentSessionID,
				ProjectID: tc.ProjectID, ToolCallID: tc.ToolCallID, ProjectDir: tc.ActiveRootPath(),
				ToolName: tool, Command: commandsurface.PrimaryCommandLine(args, nil), ProposedReadPath: path,
				ReadDenyPaths:      tools.ActionConfineInputsForContext(tc, nil).ReadDenyPaths,
				SessionScratchRoot: tc.SessionScratchDir,
			})
			return result.Authorized, result.Denied, result.UserGuidance, err
		})
	}
}

func (r *CommandServices) SetVerifyDeclaredCommand(resolve native.DeclaredVerifyCommand) {
	if r == nil {
		return
	}
	if r.verifyTool != nil {
		r.verifyTool.DeclaredCommand = resolve
	}
	if r.commandTool != nil {
		r.commandTool.DeclaredCommand = resolve
	}
}

func (r *CommandServices) backgroundCommandLine(sessionID, handle string) string {
	if r == nil || r.bgRegistry == nil {
		return ""
	}
	command, err := r.bgRegistry.CommandLine(sessionID, handle)
	if err != nil {
		return ""
	}
	return command
}
