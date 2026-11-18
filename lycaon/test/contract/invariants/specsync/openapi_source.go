package specsync

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/test/contract/invariants"

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

// sourceTree is the docs/openapi fragment tree, parsed once per corpus.
func sourceTree(r *invariants.Run) openAPISourceTree {
	return invariants.Part(r.Corpus, "openapi sources", func() (openAPISourceTree, error) {
		return loadOpenAPISourceTree(r)
	})
}

func loadOpenAPISourceTree(r *invariants.Run) (openAPISourceTree, error) {
	tree := openAPISourceTree{docs: make(map[string]any)}
	for _, f := range r.Tree("docs/openapi", ".yaml") {
		rel := filepath.ToSlash(f.Rel)
		if _, skip := openAPINonFragmentFiles[rel]; skip || strings.HasPrefix(rel, "vocab/") {
			continue // Vocabulary reaches the bundle through generated fragments.
		}
		var doc any
		if err := yaml.Unmarshal(f.Bytes(), &doc); err != nil {
			return tree, fmt.Errorf("parse %s: %w", rel, err)
		}
		tree.docs[rel] = doc
	}
	if _, ok := tree.docs[openAPISourceRoot]; !ok {
		return tree, fmt.Errorf("docs/openapi/%s missing", openAPISourceRoot)
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
	return tree, nil
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

// A definition root.yaml does not reach is invisible to every check of the
// bundled document: reference it or delete it.
func openAPISourceNoOrphanDefinitions(r *invariants.Run) {
	tree := sourceTree(r)
	reached, _ := tree.reachable()
	for _, def := range tree.definitions {
		if !openAPIDefinitionLive(reached, def) {
			r.Report("docs/openapi/%s is unreachable from root.yaml", def)
		}
	}
}

func openAPISourceRefsResolve(r *invariants.Run) {
	_, dangling := sourceTree(r).reachable()
	for _, ref := range dangling {
		r.Report("docs/openapi $ref resolves to nothing: %s", ref)
	}
}

func openAPISourceFragmentsAllLive(r *invariants.Run) {
	tree := sourceTree(r)
	reached, _ := tree.reachable()
	for file := range tree.docs {
		if file != openAPISourceRoot && len(reached[file]) == 0 {
			r.Report("docs/openapi/%s has nothing reachable from root.yaml", file)
		}
	}
}

var sourceRules = []invariants.Invariant{
	{Name: "openapi-sources-have-no-orphan-definitions", Anchors: []invariants.Anchor{sourceRoot}, Check: openAPISourceNoOrphanDefinitions},
	{Name: "openapi-source-refs-resolve", Anchors: []invariants.Anchor{sourceRoot}, Check: openAPISourceRefsResolve},
	{Name: "openapi-source-fragments-are-live", Anchors: []invariants.Anchor{sourceRoot}, Check: openAPISourceFragmentsAllLive},
}

var sourceRoot = invariants.File("docs/openapi/" + openAPISourceRoot)
