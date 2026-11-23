package command

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
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
	// unobserved lists the folders a window was due to cover when it could not
	// open; empty when one opened or none was due.
	unobserved       []api.CommandFolderCoverage
	fallbackGitRoots []gitrepo.AttachedRoot
	fallbackOutside  []string
}

// openCommandWindow opens the window for a primary-tree command. Worker
// branch commands write an overlay the promote records, so they open none.
// A failure never blocks the command; it runs unobserved.
func openCommandWindow(ctx context.Context, tctx tools.ToolContext, toolName, commandLine string) commandWindow {
	outside := outsideRootSpecs(tctx)
	fallback := commandWindow{
		fallbackGitRoots: attachedGitRoots(tctx),
		fallbackOutside:  outsidePaths(outside),
	}
	if !commandWindowDue(tctx) {
		return fallback
	}
	opener := tctx.Source.Commands
	roots := make([]sourceledger.RootSpec, 0, len(tctx.Source.Roots))
	for _, root := range tctx.Source.Roots {
		branch, err := tctx.SourceBranch(root.ID)
		if err != nil {
			fallback.unobserved = unobservedFolders(tctx, outside)
			return fallback
		}
		roots = append(roots, sourceledger.RootSpec{ID: root.ID, BranchID: branch, Path: root.Path})
	}
	open, err := opener.OpenCommandWindow(ctx, sourceledger.CommandWindowInput{
		ProjectID: tctx.Identity.ProjectID, Roots: roots, Outside: outside,
		SessionID: tctx.Identity.SessionID, Turn: tctx.Identity.UserTurn, ToolCallID: tctx.Identity.ToolCallID,
		ToolName: toolName, CommandLine: commandLine,
	})
	if err != nil {
		slog.WarnContext(ctx, "command window open", "tool", toolName, "session_id", tctx.Identity.SessionID, "err", err)
		fallback.unobserved = unobservedFolders(tctx, outside)
		return fallback
	}
	fallback.open = open
	return fallback
}

// outsideRootSpecs are the write roots beyond the project the window compares.
func outsideRootSpecs(tctx tools.ToolContext) []sourceledger.OutsideRootSpec {
	roots := tctx.OutsideWriteRoots()
	out := make([]sourceledger.OutsideRootSpec, 0, len(roots))
	for _, root := range roots {
		out = append(out, sourceledger.OutsideRootSpec{Path: root.Path, Omit: root.Omit})
	}
	return out
}

// unobservedFolders states every folder a window was due to cover as not
// observed: none had its starting state recorded before the command started.
func unobservedFolders(tctx tools.ToolContext, outside []sourceledger.OutsideRootSpec) []api.CommandFolderCoverage {
	out := make([]api.CommandFolderCoverage, 0, len(tctx.Source.Roots)+len(outside))
	for _, root := range tctx.Source.Roots {
		out = append(out, api.CommandFolderCoverage{Folder: root.Path, Coverage: api.SourceWindowCoverageNotObserved, Reason: api.SourceWindowGapBaselineIncomplete})
	}
	for _, root := range outside {
		out = append(out, api.CommandFolderCoverage{Folder: root.Path, Coverage: api.SourceWindowCoverageNotObserved, Reason: api.SourceWindowGapBaselineIncomplete})
	}
	return out
}

func coverageFact(coverage []sourceledger.FolderCoverage) []api.CommandFolderCoverage {
	out := make([]api.CommandFolderCoverage, 0, len(coverage))
	for _, folder := range coverage {
		out = append(out, folder.ActivityFact(folder.Folder))
	}
	return out
}

// noteBoundary records the confinement a process of the window ran under; an
// unconfined one could write anywhere beyond the project.
func (w commandWindow) noteBoundary(boundary confine.Boundary) {
	if w.open != nil && !boundary.Applied {
		w.open.MarkUnconfined()
	}
}

// commandWindowDue reports whether commands in this context are observed at all.
func commandWindowDue(tctx tools.ToolContext) bool {
	if tctx.Source.Commands == nil || tctx.Identity.ProjectID == "" || len(tctx.Source.Roots) == 0 {
		return false
	}
	return tctx.Source.SourceWorkspaceKind == "" || tctx.Source.SourceWorkspaceKind == api.SourceWorkspaceKindProject
}

// closeNow ends the window once the process has exited, detached from the
// tool's cancellation so an interrupted call still lands its changes.
func (w commandWindow) closeNow(ctx context.Context) {
	if w.open == nil {
		invalidateUnobservedLinkedWriteRoots(w.fallbackGitRoots, w.fallbackOutside)
		return
	}
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commandWindowCloseBudget)
	defer cancel()
	if err := w.open.Close(closeCtx); err != nil {
		slog.WarnContext(ctx, "command window close", "window_id", w.open.ID, "err", err)
		invalidateUnobservedLinkedWriteRoots(w.fallbackGitRoots, w.fallbackOutside)
	}
}

// fact states the window on a command result; nil when none was due.
func (w commandWindow) fact() *hostcmd.SourceWindow {
	if w.open == nil {
		if len(w.unobserved) > 0 {
			return &hostcmd.SourceWindow{Coverage: w.unobserved}
		}
		return nil
	}
	return &hostcmd.SourceWindow{ID: w.open.ID, Settled: w.open.IsSettled(), Coverage: coverageFact(w.open.Coverage())}
}

// closeOnExit ends the window when a promoted or background process exits.
func (w commandWindow) closeOnExit(ctx context.Context, registry *bgprocess.Registry, sessionID, handle string) {
	if w.open == nil && len(w.fallbackOutside) == 0 {
		return
	}
	if registry == nil {
		w.closeNow(ctx)
		return
	}
	background := context.WithoutCancel(ctx)
	registry.Lifecycle.OnExit(sessionID, handle, func() { w.closeNow(background) })
}

func attachedGitRoots(tctx tools.ToolContext) []gitrepo.AttachedRoot {
	var out []gitrepo.AttachedRoot
	for _, root := range tctx.Source.Roots {
		if root.External || strings.TrimSpace(root.ID) == "" || strings.TrimSpace(root.Path) == "" {
			continue
		}
		out = append(out, gitrepo.AttachedRoot{ID: root.ID, Path: root.Path})
	}
	return out
}

func outsidePaths(specs []sourceledger.OutsideRootSpec) []string {
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		if p := strings.TrimSpace(spec.Path); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func invalidateUnobservedLinkedWriteRoots(roots []gitrepo.AttachedRoot, outside []string) {
	if len(roots) == 0 || len(outside) == 0 {
		return
	}
	resolver := gitrepo.NewCheckoutResolver(roots)
	if !resolver.HasRepositories() {
		return
	}
	for _, path := range outside {
		if addr, ok := resolver.ResolvePath(path); ok {
			if addr.Rel == "" || addr.Rel == "." {
				sourcecatalog.Process().InvalidateRoot(addr.RootPath)
				continue
			}
			sourcecatalog.Process().InvalidateRoot(addr.RootPath, addr.Rel)
		}
	}
}

func stateObservation(tctx tools.ToolContext, window *hostcmd.SourceWindow) {
	if fact := observationFact(window); fact != nil {
		tctx.Effects.Out.StateCardFact(*fact)
	}
}

func observationFact(window *hostcmd.SourceWindow) *api.ActionFact {
	if window == nil {
		return nil
	}
	fact := api.ActionFact{Kind: api.ActionFactSourceObservation}
	for _, folder := range window.Coverage {
		if folder.Coverage != api.SourceWindowCoverageComplete {
			fact.SourceCoverage = append(fact.SourceCoverage, folder)
		}
	}
	if window.ID != "" && !window.Settled {
		fact.SourceWindows = []string{window.ID}
	}
	if len(fact.SourceCoverage) == 0 && len(fact.SourceWindows) == 0 {
		return nil
	}
	return &fact
}
