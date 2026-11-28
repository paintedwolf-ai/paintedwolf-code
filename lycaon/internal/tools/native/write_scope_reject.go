package native

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// writeScopeReject maps profile write-scope failures to WRITE_SCOPE_DENIED.
// Coordinator profile errors stay raw for ScopeObservation (COORDINATOR_INVESTIGATE_DENIED_PATH).
func writeScopeReject(ctx context.Context, boundary *sandbox.Boundary, path, profileID, tool string, err error) error {
	return mapWriteScopeDenied(ctx, boundary, path, profileID, tool, err, false)
}

// ProgramWriteScopeReject preserves a typed denial when a command child settles
// inside the program instead of returning its scope error to the tool executor.
func ProgramWriteScopeReject(ctx context.Context, boundary *sandbox.Boundary, profileID, tool string, err error) error {
	return mapWriteScopeDenied(ctx, boundary, "", profileID, tool, err, true)
}

func mapWriteScopeDenied(ctx context.Context, boundary *sandbox.Boundary, path, profileID, tool string, err error, mapCoordinator bool) error {
	if err == nil {
		return nil
	}
	if !mapCoordinator && errors.Is(err, sandbox.ErrPathEscape) {
		return &toolrejection.ToolReject{Code: "SURVEY_PATH_ESCAPE", Data: map[string]any{"path": path, "reason": err.Error()}}
	}
	if !mapCoordinator && profileID == coordinatorProfileID {
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
		if scopeErr.Profile == toolcontract.CoordinatorProductWriteScope {
			globs = boundary.NamedWriteGlobs(scopeErr.Profile)
		} else {
			globs = boundary.WriteGlobsForProfile(ctx, profileID)
		}
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
