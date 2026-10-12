package structure

import (
	"fmt"
	"go/parser"
	"go/token"
	"path"
	"slices"
	"strconv"
	"strings"
)

const module = "github.com/lycaon/lycaon"

// Tree is the read view of a checkout the package needs.
type Tree interface {
	Files() ([]string, error)
	Read(relative string) ([]byte, error)
}

// Graph is the production import graph of the engine's Go packages.
// Imports maps a package directory (relative to lycaon/) to the module
// packages it imports; Consumers is the inverted view; Packages is every
// package directory with at least one production file.
type Graph struct {
	Imports   map[string]map[string]bool
	Consumers map[string]map[string]bool
	Packages  map[string]bool
}

// buildGraph parses production Go files under lycaon/ and records package
// imports between module packages. Test files are excluded: the graph answers
// how the engine is wired, not how it is verified.
func buildGraph(tree Tree) (*Graph, error) {
	files, err := tree.Files()
	if err != nil {
		return nil, fmt.Errorf("list checkout: %w", err)
	}
	g := &Graph{Imports: map[string]map[string]bool{}, Consumers: map[string]map[string]bool{}, Packages: map[string]bool{}}
	for _, file := range files {
		if !isProductionGo(file) {
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
		g.Packages[pkg] = true
		for _, spec := range parsed.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
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

func isProductionGo(file string) bool {
	if !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
		return false
	}
	if !strings.HasPrefix(file, "lycaon/internal/") && !strings.HasPrefix(file, "lycaon/cmd/") && !strings.HasPrefix(file, "lycaon/pkg/") {
		return false
	}
	return !slices.Contains(strings.Split(file, "/"), "testdata")
}

// outsideConsumers lists packages outside root that import member.
func (g *Graph) outsideConsumers(root, member string) []string {
	var out []string
	for consumer := range g.Consumers[member] {
		if !within(root, consumer) {
			out = append(out, consumer)
		}
	}
	slices.Sort(out)
	return out
}

func within(root, dir string) bool {
	return dir == root || strings.HasPrefix(dir, root+"/")
}

// members lists the direct package children of root, then the directory
// itself when it carries production files. Deeper packages belong to their
// own directory's diagnostics.
func (g *Graph) members(root string) []string {
	seen := map[string]bool{}
	for pkg := range g.Packages {
		if pkg == root || (within(root, pkg) && !strings.Contains(strings.TrimPrefix(pkg, root+"/"), "/")) {
			seen[pkg] = true
		}
	}
	out := make([]string, 0, len(seen))
	for pkg := range seen {
		out = append(out, pkg)
	}
	slices.Sort(out)
	return out
}
