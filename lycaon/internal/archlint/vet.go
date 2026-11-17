package archlint

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/checker"
	"golang.org/x/tools/go/packages"
)

// Vet runs per-package analyzers over the non-test packages matching
// patterns. A finding's key is its analyzer, package and message, which name
// symbols and never lines.
func Vet(dir string, patterns []string, analyzers []*analysis.Analyzer) ([]Finding, error) {
	initial, err := packages.Load(&packages.Config{Dir: dir, Mode: packages.LoadAllSyntax}, patterns...)
	if err != nil {
		return nil, err
	}
	if packages.PrintErrors(initial) > 0 {
		return nil, fmt.Errorf("packages contain errors")
	}
	graph, err := checker.Analyze(analyzers, initial, nil)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	var out []Finding
	for act := range graph.All() {
		if !act.IsRoot {
			continue
		}
		if act.Err != nil {
			return nil, fmt.Errorf("%s: %w", act, act.Err)
		}
		for _, d := range act.Diagnostics {
			pos := act.Package.Fset.Position(d.Pos)
			if rel, err := filepath.Rel(abs, pos.Filename); err == nil && !strings.HasPrefix(rel, "..") {
				pos.Filename = rel
			}
			out = append(out, Finding{
				Kind:    act.Analyzer.Name,
				Key:     act.Package.PkgPath + ": " + d.Message,
				Pos:     pos.String(),
				Message: d.Message,
			})
		}
	}
	return out, nil
}
