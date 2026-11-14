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
	if resp.rootID != "" && resp.scopeRel != "" {
		tctx.RecordSourceIdentity(resp.rootID, resp.scopeRel, api.NavigationEntryKindFolder)
	} else {
		tctx.RecordSourcePath(resp.Path, api.NavigationEntryKindFolder)
	}
	for _, entry := range resp.Entries {
		if entry.rootID != "" && entry.scopeRel != "" {
			tctx.RecordSourceIdentity(entry.rootID, entry.scopeRel, navigationEntryKind(entry.Type))
			continue
		}
		tctx.RecordSourcePath(entry.Path, navigationEntryKind(entry.Type))
	}
	var visit func(*directoryMapNode)
	visit = func(node *directoryMapNode) {
		if node == nil {
			return
		}
		if node.rootID != "" && node.scopeRel != "" {
			tctx.RecordSourceIdentity(node.rootID, node.scopeRel, navigationEntryKind(node.Type))
		} else {
			tctx.RecordSourcePath(node.Path, navigationEntryKind(node.Type))
		}
		for _, child := range node.Children {
			visit(child)
		}
	}
	visit(resp.Tree)
}
