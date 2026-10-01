package survey

import (
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func navigationEntryKind(kind string) api.NavigationEntryKind {
	if kind == "dir" || kind == "directory" {
		return api.NavigationEntryKindFolder
	}
	return api.NavigationEntryKindFile
}

func recordListedSources(tctx tools.ToolContext, resp listDirResponse) {
	tctx.RecordSourcePath(resp.Path, api.NavigationEntryKindFolder)
	for _, entry := range resp.Entries {
		tctx.RecordSourcePath(entry.Path, navigationEntryKind(entry.Type))
	}
	var visit func(*directoryMapNode)
	visit = func(node *directoryMapNode) {
		if node == nil {
			return
		}
		tctx.RecordSourcePath(node.Path, navigationEntryKind(node.Type))
		for _, child := range node.Children {
			visit(child)
		}
	}
	visit(resp.Tree)
}
