package toolexecution

import (
	"github.com/lycaon/lycaon/internal/tools"

	"github.com/lycaon/lycaon/internal/fspath"
)

// Unresolved entries remain empty so partial resolution cannot widen a grant.
func ResolvedApprovalFiles(tool string, args map[string]any, tc tools.ToolContext) []string {
	files := filesFromArgs(tool, args)
	if len(files) == 0 {
		return nil
	}
	resolved := make([]string, len(files))
	for i, path := range files {
		abs, err := tools.ApprovalFilePath(tc, path)
		if err == nil {
			resolved[i] = fspath.CanonicalPath(abs)
		}
	}
	return resolved
}
