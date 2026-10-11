package native

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
)

// writeScopeReject maps profile write-scope failures to WRITE_SCOPE_DENIED.
// Coordinator profile errors stay raw for ScopeObservation (COORDINATOR_INVESTIGATE_DENIED_PATH).
func writeScopeReject(ctx context.Context, boundary *sandbox.Boundary, path, profileID, tool string, err error) error {
	return mapWriteScopeDenied(ctx, boundary, path, profileID, tool, err, writeScopeMapOpts{})
}

type writeScopeMapOpts struct {
	kind           string
	mapCoordinator bool
}

func mapWriteScopeDenied(ctx context.Context, boundary *sandbox.Boundary, path, profileID, tool string, err error, opts writeScopeMapOpts) error {
	if err == nil {
		return nil
	}
	if !opts.mapCoordinator && profileID == coordinatorProfileID {
		return err
	}
	var scopeErr *sandbox.ScopeError
	if !errors.As(err, &scopeErr) || scopeErr.Kind != sandbox.ScopeWrite {
		return err
	}
	if path == "" {
		path = scopeErr.Path
	}
	var globs []string
	if boundary != nil {
		globs = boundary.WriteGlobsForProfile(ctx, profileID)
	}
	if profileID == "" {
		profileID = toolprofiles.DefaultToolProfileID
	}
	data := map[string]any{
		"path":           path,
		"tool":           tool,
		"profile":        profileID,
		"patterns_list":  formatGlobsMarkdown(globs),
		"patterns_count": len(globs),
	}
	if opts.kind != "" {
		data["kind"] = opts.kind
	}
	return &toolrejection.ToolReject{
		Code: "WRITE_SCOPE_DENIED",
		Data: data,
	}
}

// formatGlobsMarkdown renders write-scope globs as a markdown bullet list.
func formatGlobsMarkdown(globs []string) string {
	var b strings.Builder
	for _, g := range globs {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		fmt.Fprintf(&b, "- `%s`\n", g)
	}
	return strings.TrimRight(b.String(), "\n")
}
