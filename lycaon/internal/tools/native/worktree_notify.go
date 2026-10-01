package native

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/tools"
)

// afterSuccessfulMutation runs worker mutation hooks, emits WorktreeChanged,
// and notifies the blueprint write observer.
func afterSuccessfulMutation(ctx context.Context, tctx tools.ToolContext, paths ...string) {
	tools.ReportSyntaxOverride(ctx, tctx, paths...)
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		afterWorkerMutation(ctx, tctx, p)
	}
	notifyWorktreeChanged(ctx, tctx, paths...)
	notifyBlueprintWrite(ctx, tctx, paths...)
}

// notifyWorktreeChanged debounces a WorktreeChanged event for the active root.
func notifyWorktreeChanged(ctx context.Context, tctx tools.ToolContext, paths ...string) {
	root := strings.TrimSpace(tools.HostWriteRoot(tctx))
	if root == "" {
		return
	}
	cleaned := make([]string, 0, len(paths))
	for _, p := range paths {
		p = filepath.ToSlash(strings.TrimSpace(p))
		if p == "" || p == "." {
			continue
		}
		cleaned = append(cleaned, p)
	}
	sourcecatalog.Process().InvalidateRoot(root, cleaned...) //nolint:contextcheck // Catalog refresh outlives the tool call.
	repochange.NotifyWorktreeDebounced(ctx, root, cleaned, repochange.SourceMutation)
}
