package toolhost

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/tools/native/command"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

// CommandServices owns native command configuration and background lifecycle.
type CommandServices struct {
	owners            programOwnerTools
	bgRegistry        *bgprocess.Registry
	commandTool       *command.CommandTool
	verifyTool        *native.VerifyTool
	commandOutputTool *command.CommandOutputTool
	commandStopTool   *command.CommandStopTool
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
	if programs := r.programs(); programs != nil {
		programs.Supervisor.Background = reg
	}
	if r.reviews != nil {
		r.reviews.SetBackgroundCommandResolver(r.backgroundCommandLine)
	}
}

func (r *CommandServices) SetSandboxWriteRootGate(writeRoot command.SandboxWriteRootGate) {
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
			result, err := writeRoot.Authorize(ctx, command.SandboxWriteRootAsk{
				SessionID: tc.Identity.SessionID, ParentSessionID: tc.Identity.ParentSessionID,
				ProjectID: tc.Identity.ProjectID, ToolCallID: tc.Identity.ToolCallID, ProjectDir: tc.ActiveRootPath(),
				ToolName: tool, Command: commandsurface.PrimaryCommandLine(args, nil), ProposedWriteRoot: root,
				SessionScratchRoot: tc.Host.SessionScratchDir,
				SessionOutputRoot:  tc.Host.SessionOutputDir,
			})
			return result.Authorized, result.Denied, result.UserGuidance, err
		})
		r.paths.SetSessionReadPathOverlay(writeRoot.SessionReadPaths)
		r.paths.SetReadPathPreflight(func(ctx context.Context, tool string, args map[string]any, tc tools.ToolContext, path string) (bool, bool, string, error) {
			result, err := writeRoot.AuthorizeRead(ctx, command.SandboxReadPathAsk{
				SessionID: tc.Identity.SessionID, ParentSessionID: tc.Identity.ParentSessionID,
				ProjectID: tc.Identity.ProjectID, ToolCallID: tc.Identity.ToolCallID, ProjectDir: tc.ActiveRootPath(),
				ToolName: tool, Command: commandsurface.PrimaryCommandLine(args, nil), ProposedReadPath: path,
				ReadDenyPaths:      tools.ActionConfineInputsForContext(tc, nil).ReadDenyPaths,
				SessionScratchRoot: tc.Host.SessionScratchDir,
				SessionOutputRoot:  tc.Host.SessionOutputDir,
			})
			return result.Authorized, result.Denied, result.UserGuidance, err
		})
	}
}

func (r *CommandServices) SetVerifyDeclaredCommand(resolve command.DeclaredVerifyCommand) {
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

// ProgramServices are the host owners the command program entry composes.
type ProgramServices struct {
	Recorder  *invocation.SQLRecorder
	Blobs     blobstore.Store
	Projector *captureprojection.Projector
	Progress  toolexecution.ProgramProgressFacts
	Sessions  command.SessionOwners
	// SourceRuns records the run evidence of a program that ended parked.
	SourceRuns command.SourceRunRecorder
	// ExternalHidden withdraws the explicit external selector.
	ExternalHidden bool
}

func (r *CommandServices) programs() *command.ProgramRuntime {
	if r == nil || r.commandTool == nil {
		return nil
	}
	return r.commandTool.Programs
}

// SetProgramServices completes the program runtime at host composition. An
// owner that cannot be composed fails host startup rather than leaving
// programs without their native utilities.
func (r *CommandServices) SetProgramServices(services ProgramServices) error {
	programs := r.programs()
	if programs == nil {
		return nil
	}
	programs.Recorder, programs.Blobs, programs.Projector = services.Recorder, services.Blobs, services.Projector
	programs.Progress, programs.Sessions, programs.SourceRuns = services.Progress, services.Sessions, services.SourceRuns
	programs.Supervisor.Interrupter = services.Recorder
	programs.ExternalHidden = services.ExternalHidden
	if programs.Executor != nil {
		programs.Executor.Metadata.SetCommandExternalHidden(services.ExternalHidden)
	}
	owners, err := r.owners.compose(services.Recorder)
	if err != nil {
		return fmt.Errorf("compose command program owners: %w", err)
	}
	programs.Owners = owners
	return nil
}

// SetProgramParking lets command calls whose program waits park their turn.
func (r *CommandServices) SetProgramParking(parking tools.ProgramParking) {
	if programs := r.programs(); programs != nil {
		programs.Parking = parking
	}
}

// ProgramReceiptResult reports a program the host no longer runs from its
// durable receipts.
func (r *CommandServices) ProgramReceiptResult(ctx context.Context, sessionID, programID string) (string, error) {
	programs := r.programs()
	if programs == nil {
		return "", fmt.Errorf("command program runtime is not configured")
	}
	return programs.ReceiptResult(ctx, sessionID, programID)
}

// setProgramExecutor binds the executor that admits each native child.
func (r *CommandServices) setProgramExecutor(executor *toolexecution.Executor) {
	if programs := r.programs(); programs != nil {
		programs.Executor = executor
		executor.SetProgramEntry("command", command.CaptureInvocation)
	}
}

// DisposeProgramSession releases the session's program job owner after closing
// admission and draining its registered jobs.
func (r *CommandServices) DisposeProgramSession(ctx context.Context, sessionID string) error {
	if programs := r.programs(); programs != nil {
		return programs.Supervisor.DisposeSession(ctx, sessionID)
	}
	return nil
}
