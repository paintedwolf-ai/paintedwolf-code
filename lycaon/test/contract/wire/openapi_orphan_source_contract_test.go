package contract

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

const openAPISourceRoot = "root.yaml"

// openAPINonFragmentFiles are files under docs/openapi outside the $ref graph.
var openAPINonFragmentFiles = map[string]struct{}{
	"baseline-bundle.yaml": {},
}

// Direct children of these sections are referenceable definitions.
var openAPIDefinitionSections = [][]string{
	{"paths"},
	{"components", "schemas"},
	{"components", "parameters"},
	{"components", "responses"},
	{"components", "requestBodies"},
	{"components", "headers"},
	{"components", "securitySchemes"},
}

type openAPISourceRef struct {
	File    string // Slash path relative to docs/openapi.
	Pointer string // JSON pointer within the file.
}

func (r openAPISourceRef) String() string { return r.File + "#" + r.Pointer }

type openAPISourceTree struct {
	docs        map[string]any
	definitions []openAPISourceRef
}

func loadOpenAPISourceTree(t *testing.T) openAPISourceTree {
	t.Helper()
	dir := filepath.Join(contractcheck.RepoRoot(t), "docs", "openapi")
	tree := openAPISourceTree{docs: make(map[string]any)}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".yaml") {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if _, skip := openAPINonFragmentFiles[rel]; skip {
			return nil
		}
		if strings.HasPrefix(rel, "vocab/") {
			return nil // Vocabulary reaches the bundle through generated fragments.
		}
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		var doc any
		if unmarshalErr := yaml.Unmarshal(data, &doc); unmarshalErr != nil {
			return fmt.Errorf("parse %s: %w", rel, unmarshalErr)
		}
		tree.docs[rel] = doc
		return nil
	})
	contractcheck.FailErr(t, "walk docs/openapi", err)
	if _, ok := tree.docs[openAPISourceRoot]; !ok {
		t.Fatalf("docs/openapi/%s missing", openAPISourceRoot)
	}
	for file, doc := range tree.docs {
		if file == openAPISourceRoot {
			continue // The entrypoint is always reachable.
		}
		for _, section := range openAPIDefinitionSections {
			node, ok := openAPINodeAt(doc, section)
			if !ok {
				continue
			}
			members, ok := node.(map[string]any)
			if !ok {
				continue
			}
			for name := range members {
				tree.definitions = append(tree.definitions, openAPISourceRef{
					File:    file,
					Pointer: "/" + strings.Join(append(append([]string{}, section...), openAPIEscapeToken(name)), "/"),
				})
			}
		}
	}
	sort.Slice(tree.definitions, func(i, j int) bool {
		return tree.definitions[i].String() < tree.definitions[j].String()
	})
	return tree
}

func (tree openAPISourceTree) reachable() (reached map[string][]string, dangling []string) {
	reached = make(map[string][]string)
	seen := make(map[openAPISourceRef]struct{})
	var danglingSet []string

	queue := []openAPISourceRef{{File: openAPISourceRoot, Pointer: ""}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if _, done := seen[cur]; done {
			continue
		}
		seen[cur] = struct{}{}

		doc, ok := tree.docs[cur.File]
		if !ok {
			danglingSet = append(danglingSet, cur.String()+" (no such fragment file)")
			continue
		}
		node, ok := openAPINodeAt(doc, openAPIPointerTokens(cur.Pointer))
		if !ok {
			danglingSet = append(danglingSet, cur.String()+" (pointer not found)")
			continue
		}
		reached[cur.File] = append(reached[cur.File], cur.Pointer)
		for _, raw := range openAPICollectRefs(node) {
			target, ok := resolveOpenAPIRef(cur.File, raw)
			if !ok {
				danglingSet = append(danglingSet, fmt.Sprintf("%s: unparsable $ref %q", cur.File, raw))
				continue
			}
			queue = append(queue, target)
		}
	}
	sort.Strings(danglingSet)
	return reached, danglingSet
}

// Referencing a parent pointer also includes its descendant definitions.
func openAPIDefinitionLive(reached map[string][]string, def openAPISourceRef) bool {
	for _, pointer := range reached[def.File] {
		if pointer == "" || pointer == def.Pointer || strings.HasPrefix(def.Pointer, pointer+"/") {
			return true
		}
	}
	return false
}

func resolveOpenAPIRef(fromFile, raw string) (openAPISourceRef, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "://") {
		return openAPISourceRef{}, false
	}
	filePart, pointer, _ := strings.Cut(raw, "#")
	if filePart == "" {
		return openAPISourceRef{File: fromFile, Pointer: pointer}, true
	}
	return openAPISourceRef{
		File:    path.Clean(path.Join(path.Dir(fromFile), filePart)),
		Pointer: pointer,
	}, true
}

func openAPICollectRefs(node any) []string {
	var out []string
	switch typed := node.(type) {
	case map[string]any:
		for key, value := range typed {
			if key == "$ref" {
				if s, ok := value.(string); ok {
					out = append(out, s)
					continue
				}
			}
			out = append(out, openAPICollectRefs(value)...)
		}
	case []any:
		for _, item := range typed {
			out = append(out, openAPICollectRefs(item)...)
		}
	}
	return out
}

func openAPINodeAt(doc any, tokens []string) (any, bool) {
	node := doc
	for _, token := range tokens {
		asMap, ok := node.(map[string]any)
		if !ok {
			return nil, false
		}
		node, ok = asMap[token]
		if !ok {
			return nil, false
		}
	}
	return node, true
}

func openAPIPointerTokens(pointer string) []string {
	pointer = strings.TrimPrefix(pointer, "/")
	if pointer == "" {
		return nil
	}
	parts := strings.Split(pointer, "/")
	for i, part := range parts {
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
	}
	return parts
}

func openAPIEscapeToken(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

// Unreferenced definitions are invisible to checks of the bundled specification.
func TestOpenAPISourceNoOrphanDefinitions(t *testing.T) {
	t.Parallel()
	tree := loadOpenAPISourceTree(t)
	reached, _ := tree.reachable()
	var violations []string
	for _, def := range tree.definitions {
		if !openAPIDefinitionLive(reached, def) {
			violations = append(violations, def.String())
		}
	}
	contractcheck.FailViolations(t, "docs/openapi definitions unreachable from root.yaml"+
		"\nfix: $ref the definition from root.yaml (or from a reachable fragment),"+
		" or delete the unused definition", violations)
}

func TestOpenAPISourceRefsResolve(t *testing.T) {
	t.Parallel()
	tree := loadOpenAPISourceTree(t)
	_, dangling := tree.reachable()
	contractcheck.FailViolations(t, "docs/openapi $refs that resolve to nothing"+
		"\nfix: correct the path or pointer, or add the missing definition", dangling)
}

func TestOpenAPISourceFragmentsAllLive(t *testing.T) {
	t.Parallel()
	tree := loadOpenAPISourceTree(t)
	reached, _ := tree.reachable()
	var violations []string
	for file := range tree.docs {
		if file == openAPISourceRoot || len(reached[file]) > 0 {
			continue
		}
		violations = append(violations, file)
	}
	contractcheck.FailViolations(t, "docs/openapi fragment files with nothing reachable from root.yaml"+
		"\nfix: reference the fragment from root.yaml, or delete the unused fragment", violations)
}
