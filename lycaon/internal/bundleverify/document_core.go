package bundleverify

import (
	"context"
	"path/filepath"
	"strings"
	"time"
)

// checkDocumentCore runs the packaged engine's document-core probe: the engine
// resolves its sibling core, confines it, and round-trips a document, all
// under the signatures and entitlements the bundle ships with.
func checkDocumentCore(ctx context.Context, runner Runner, opts Options) []Finding {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, stderr, err := runner.Run(ctx, filepath.Join(opts.AppPath, sidecarRelPath), "diagnostics", "document-core")
	if err == nil {
		return nil
	}
	return []Finding{{Code: CodeDocumentCoreFailed, Severity: SeverityError, Path: documentCoreRelPath,
		Detail: map[string]string{"reason": err.Error(), "output": strings.TrimSpace(string(stderr))}}}
}
