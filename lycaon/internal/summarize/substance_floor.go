package summarize

import (
	"context"
	"strings"
)

// applySubstanceFloor adds one citable directory window.
func (e *Engine) applySubstanceFloor(ctx context.Context, task string, caps Caps, subtree *SubtreeNode, importance SubtreeImportance, pack *ContextPack, stats *CuratorStats) {
	if !caps.Pack.SubtreeSubstanceFloor || pack == nil || len(pack.Substance) > 0 {
		return
	}
	if subtree == nil || e.Outliner == nil {
		return
	}
	file := floorRepresentativeFile(task, subtree, importance)
	if file == "" {
		return
	}
	sc, ok := e.Outliner.Outline(ctx, file)
	if !ok || len(sc.Symbols) == 0 {
		return
	}
	defs := rankDefinitions(ctx, e.Rerank, task, buildDefinitionPile([]StructureCandidate{sc}))
	windowLines := caps.Gather.SymbolWindowLines
	for _, d := range defs {
		if d.Pinned || d.Kind == "directory_map" || d.Kind == KindDirectoryRollup {
			continue
		}
		w, wok := symbolWindowLines(d, windowLines, nil)
		if !wok {
			continue
		}
		ensureFloorIdentity(pack, d)
		pack.Skeleton = append(pack.Skeleton, PackSymbol{Path: d.RelPath, Kind: d.Kind, Name: d.Name, Line: d.Line})
		pack.Substance = append(pack.Substance, w)
		if stats != nil {
			stats.FilesOutlined++
			stats.DepthAdmits++
			stats.DeepenHits++
			stats.BudgetTokensSpent += caps.EstimateTokens(windowLine(w))
		}
		return
	}
}

// floorRepresentativeFile selects a leaf from the highest-ranked child.
func floorRepresentativeFile(task string, subtree *SubtreeNode, importance SubtreeImportance) string {
	for _, c := range rankChildrenImportance(subtree, importance, task) {
		if c == nil || strings.HasPrefix(c.Path, "+") {
			continue
		}
		if f := RepresentativeFilePath(c); f != "" {
			return f
		}
	}
	return RepresentativeFilePath(subtree)
}

// ensureFloorIdentity pairs a drilled window with file identity.
func ensureFloorIdentity(pack *ContextPack, d DefinitionItem) {
	for _, id := range pack.Identity {
		if id.Path == d.RelPath {
			return
		}
	}
	health := d.ParseHealth
	if health == "" {
		health = parseHealthOK
	}
	pack.Identity = append(pack.Identity, PackIdentity{
		Path: d.RelPath, Kind: StructureKindFile, LineCount: d.LineCount,
		ParseHealth: health, ContentHash: d.ContentHash, Language: d.Language,
		OutlineSource: d.OutlineSource, Parses: d.Parses, Errors: d.Errors,
		ErrorKind: d.ErrorKind, ImportPath: d.ImportPath,
	})
}
