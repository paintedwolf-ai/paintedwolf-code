package inject

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/pkg/api"
)

// ScopeFileSymbol is one outline entry for worker task injection.
type ScopeFileSymbol struct {
	Kind string
	Name string
	Line int
}

// ScopeFileOrientation is precomputed survey for a scoped file path.
type ScopeFileOrientation struct {
	Path       string
	TotalLines int
	SizeBytes  int64
	Source     string
	Symbols    []ScopeFileSymbol
	// SymbolsElided counts outline entries past MaxScopeOutlineSymbols.
	SymbolsElided int
}

// BuildScopeFileOrientations returns structural outlines for literal scoped file paths.
func BuildScopeFileOrientations(ctx context.Context, projectDir string, scope api.TaskScope) ([]ScopeFileOrientation, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return nil, nil
	}
	norm := scope.Normalized()
	var out []ScopeFileOrientation
	seen := map[string]struct{}{}
	for _, raw := range norm.Paths {
		rel := filepath.ToSlash(strings.TrimSpace(raw))
		if rel == "" || strings.ContainsAny(rel, "*?[") {
			continue
		}
		if _, ok := seen[rel]; ok {
			continue
		}
		seen[rel] = struct{}{}
		full := filepath.Join(projectDir, filepath.FromSlash(rel))
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			continue
		}
		outline, err := fileoutline.Build(ctx, projectDir, rel)
		if err != nil {
			continue
		}
		orient := ScopeFileOrientation{
			Path:       rel,
			TotalLines: outline.TotalLines,
			SizeBytes:  outline.SizeBytes,
			Source:     outline.Source,
		}
		symbols, elided := bound(outline.Symbols, MaxScopeOutlineSymbols)
		orient.SymbolsElided = elided
		for _, sym := range symbols {
			orient.Symbols = append(orient.Symbols, ScopeFileSymbol{
				Kind: sym.Kind,
				Name: sym.Name,
				Line: sym.Line,
			})
		}
		out = append(out, orient)
		if len(out) >= MaxScopeFileOrientations {
			break
		}
	}
	return out, nil
}
