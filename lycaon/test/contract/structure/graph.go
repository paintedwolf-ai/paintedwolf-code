package structure

import (
	"fmt"
	"go/parser"
	"go/token"
	"path"
	"sort"
	"strings"
)

const module = "github.com/lycaon/lycaon"

// Tree is the read view of a checkout the package needs.
type Tree interface {
	Files() []string
	Read(relative string) ([]byte, error)
}

// Graph is the production import graph of the engine's Go packages.
// Imports maps a package directory to the module packages it imports;
// Consumers is the inverted view.
type Graph struct {
	Imports   map[string]map[string]bool
	Consumers map[string]map[string]bool
}

// buildGraph parses production Go files under lycaon/ and records package
// imports between module packages. Test and contract-test files are excluded:
// the graph answers how the engine is wired, not how it is verified.
func buildGraph(tree Tree) (*Graph, error) {
	g := &Graph{Imports: map[string]map[string]bool{}, Consumers: map[string]map[string]bool{}}
	for _, file := range tree.Files() {
		if !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") ||
			!strings.HasPrefix(file, "lycaon/") || strings.HasPrefix(file, "lycaon/test/") ||
			strings.Contains(file, "/testdata/") {
			continue
		}
		body, err := tree.Read(file)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", file, err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, body, parser.ImportsOnly)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", file, err)
		}
		pkg := path.Dir(strings.TrimPrefix(file, "lycaon/"))
		for _, spec := range parsed.Imports {
			imported := strings.Trim(spec.Path.Value, `"`)
			if !strings.HasPrefix(imported, module+"/") {
				continue
			}
			dependency := strings.TrimPrefix(imported, module+"/")
			if dependency == pkg || g.Imports[pkg][dependency] {
				continue
			}
			if g.Imports[pkg] == nil {
				g.Imports[pkg] = map[string]bool{}
			}
			if g.Consumers[dependency] == nil {
				g.Consumers[dependency] = map[string]bool{}
			}
			g.Imports[pkg][dependency] = true
			g.Consumers[dependency][pkg] = true
		}
	}
	return g, nil
}

// outsideConsumers lists packages outside root that import a member of root.
func (g *Graph) outsideConsumers(root, member string) []string {
	var out []string
	for consumer := range g.Consumers[member] {
		if !within(root, consumer) {
			out = append(out, consumer)
		}
	}
	sort.Strings(out)
	return out
}

func within(root, dir string) bool {
	return dir == root || strings.HasPrefix(dir, root+"/")
}

// members lists the packages under root, the directory itself first when it
// carries its own files.
func (g *Graph) members(root string) []string {
	seen := map[string]bool{}
	if len(g.Imports[root]) > 0 || len(g.Consumers[root]) > 0 {
		seen[root] = true
	}
	for pkg := range g.Imports {
		if pkg != root && within(root, pkg) {
			seen[pkg] = true
		}
	}
	for pkg := range g.Consumers {
		if pkg != root && within(root, pkg) {
			seen[pkg] = true
		}
	}
	out := make([]string, 0, len(seen))
	for pkg := range seen {
		out = append(out, pkg)
	}
	sort.Strings(out)
	return out
}