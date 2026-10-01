package survey

import (
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
)

func listDirHasExpressedScope(args map[string]any, relPath string) bool {
	if _, ok := args["offset"]; ok {
		return true
	}
	if _, ok := args["max_depth"]; ok {
		return true
	}
	if _, ok := args["max_entries"]; ok {
		return true
	}
	return !isListDirBareRootPath(relPath)
}

func isListDirBareRootPath(relPath string) bool {
	p := strings.TrimSpace(strings.ReplaceAll(relPath, "\\", "/"))
	return projectroot.IsUnionDiscoveryPath(p)
}
