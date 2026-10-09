package native

import (
	"context"
	"log/slog"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// commandWindowCloseBudget bounds the inline pass a close runs when watcher
// coverage is incomplete.
const commandWindowCloseBudget = 45 * time.Second

// commandWindow is one dispatch's observation window; a nil handle makes
// every method a no-op.
type commandWindow struct {
	open *sourceledger.OpenCommandWindow
}

// openCommandWindow opens the window for a primary-tree command. Worker
// branch commands write an overlay the promote records, so they open none.
// A failure never blocks the command; it runs unobserved.
func openCommandWindow(ctx context.Context, tctx tools.ToolContext, toolName, commandLine string) commandWindow {
	opener := tctx.Source.Commands
	if opener == nil || tctx.Identity.ProjectID == "" || len(tctx.Source.Roots) == 0 {
		return commandWindow{}
	}
	if tctx.Source.SourceWorkspaceKind != "" && tctx.Source.SourceWorkspaceKind != api.SourceWorkspaceKindProject {
		return commandWindow{}
	}
	roots := make([]sourceledger.RootSpec, 0, len(tctx.Source.Roots))
	for _, root := range tctx.Source.Roots {
		branch, err := tctx.SourceBranch(root.ID)
		if err != nil {
			return commandWindow{}
		}
		roots = append(roots, sourceledger.RootSpec{ID: root.ID, BranchID: branch, Path: root.Path})
	}
	open, err := opener.OpenCommandWindow(ctx, sourceledger.CommandWindowInput{
		ProjectID: tctx.Identity.ProjectID, Roots: roots,
		SessionID: tctx.Identity.SessionID, Turn: tctx.Identity.UserTurn, ToolCallID: tctx.Identity.ToolCallID,
		ToolName: toolName, CommandLine: commandLine,
	})
	if err != nil {
		slog.WarnContext(ctx, "command window open", "tool", toolName, "session_id", tctx.Identity.SessionID, "err", err)
		return commandWindow{}
	}
	return commandWindow{open: open}
}

// closeNow ends the window once the process has exited, detached from the
// tool's cancellation so an interrupted call still lands its changes.
func (w commandWindow) closeNow(ctx context.Context) {
	if w.open == nil {
		return
	}
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commandWindowCloseBudget)
	defer cancel()
	if err := w.open.Close(closeCtx); err != nil {
		slog.WarnContext(ctx, "command window close", "window_id", w.open.ID, "err", err)
	}
}

// closeOnExit ends the window when a promoted or background process exits.
func (w commandWindow) closeOnExit(ctx context.Context, registry *bgprocess.Registry, sessionID, handle string) {
	if w.open == nil {
		return
	}
	if registry == nil {
		w.closeNow(ctx)
		return
	}
	background := context.WithoutCancel(ctx)
	registry.OnExit(sessionID, handle, func() { w.closeNow(background) })
}
