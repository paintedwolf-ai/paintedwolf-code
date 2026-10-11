package search

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

type codeIndexSelection struct {
	include bool
	paths   []string
}

// Literal positive paths request only the named lazy subtree, alongside ordinary source.
func codeIndexSelections(ctx context.Context, catalog *sourcecatalog.Catalog, root CodeRoot, query Node, flags MatchFlags, include bool) []codeIndexSelection {
	if include {
		return []codeIndexSelection{{include: true}}
	}
	targets := append(positivePathTargets(query, false), flags.Include...)
	var selected []string
	for _, target := range targets {
		target = strings.TrimSpace(target)
		if strings.ContainsAny(target, "*?[") || filepath.IsAbs(target) {
			continue
		}
		clean := filepath.ToSlash(filepath.Clean(target))
		if clean != target || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			continue
		}
		info, err := os.Stat(filepath.Join(root.Path, filepath.FromSlash(clean)))
		if err != nil || catalog.BoundaryPath(ctx, root.Path, clean, info.IsDir()) == "" {
			continue
		}
		selected = append(selected, clean)
	}
	slices.Sort(selected)
	selected = slices.Compact(selected)
	kept := selected[:0]
	for _, selectedPath := range selected {
		if len(kept) > 0 && strings.HasPrefix(selectedPath, kept[len(kept)-1]+"/") {
			continue
		}
		kept = append(kept, selectedPath)
	}
	result := []codeIndexSelection{{}}
	if len(kept) > 0 {
		result = append(result, codeIndexSelection{paths: kept})
	}
	return result
}
