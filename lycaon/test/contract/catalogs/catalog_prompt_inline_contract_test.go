package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// Shorter windows collide with common identifiers and phrases.
const catalogCopyWindow = 40

// Shared transport markers and retrieval framing require matching catalog text.
var catalogCopyDuplicationAllowed = map[string]string{
	"lycaon/internal/guidance/host_markers.go": "the host marker vocabulary — AGENTS.md § No heuristics " +
		"names this file as the single spelling for markers the host writes and reads back, so the " +
		"catalog and the constant are one vocabulary rather than two copies",
	"lycaon/internal/datamark/datamark.go": "the untrusted-retrieval teaching is rendered into every " +
		"framed body as well as the prompt partial, so it has to exist at the injection point; " +
		"TestUntrustedTeachingMatchesPartial pins the two equal",
}

const untrustedOutputPartial = "lycaon/config/packs/painted-wolf/web-research/shared/partials/untrusted-output.md"

func TestProductionGoHoldsNoCatalogPromptCopy(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	windows := catalogCopyWindows(t, root)
	if len(windows) == 0 {
		t.Fatal("indexed no catalog copy — the derivation is broken and this contract " +
			"would accept any prompt copy in Go")
	}

	var hits []string
	err := filepath.WalkDir(filepath.Join(root, "lycaon", "internal"), func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if name := d.Name(); name == "testdata" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel := filepath.ToSlash(mustRel(t, root, path))
		if _, allowed := catalogCopyDuplicationAllowed[rel]; allowed {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			source, matched := matchCatalogCopy(value, windows)
			if !matched {
				return true
			}
			hits = append(hits, rel+":"+strconv.Itoa(fset.Position(lit.Pos()).Line)+
				" restates catalog copy from "+source)
			return true
		})
		return nil
	})
	contractcheck.FailErr(t, "walk internal for catalog copy in Go", err)

	contractcheck.FailViolations(t, "production Go restates copy the catalog already ships — the agent reads "+
		"one of them and nothing renders both, so they drift apart unnoticed. Render the catalog "+
		"body instead of restating it", contractcheck.DedupeStrings(hits))
}

func TestCatalogCopyAllowancesAreLive(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	windows := catalogCopyWindows(t, root)

	var stale []string
	for rel, rationale := range catalogCopyDuplicationAllowed {
		if strings.TrimSpace(rationale) == "" {
			stale = append(stale, rel+": allowance carries no rationale")
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			stale = append(stale, rel+": allowed file does not exist")
			continue
		}
		if _, matched := matchCatalogCopy(string(data), windows); !matched {
			stale = append(stale, rel+": no longer restates any catalog copy — drop the allowance")
		}
	}
	sort.Strings(stale)
	contractcheck.FailViolations(t, "stale catalog-copy allowances", stale)
}

// Retrieval bodies and prompts carry the same trust notice.
func TestUntrustedTeachingMatchesPartial(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	partial := strings.TrimSpace(contractcheck.ReadRepoFile(t, root, untrustedOutputPartial))
	if partial == "" {
		t.Fatalf("%s is empty", untrustedOutputPartial)
	}
	source := contractcheck.ReadRepoFile(t, root, "lycaon/internal/datamark/datamark.go")
	if !strings.Contains(source, strconv.Quote(partial)) {
		t.Fatalf("datamark.go no longer carries the untrusted-output partial verbatim.\n"+
			"The constant is rendered into every framed retrieval body while the partial is rendered "+
			"into the prompt, so an agent that reads one and not the other gets different teaching.\n"+
			"partial: %s\nExpected a Go literal equal to: %q", untrustedOutputPartial, partial)
	}
}

// Prefix indexing finds catalog text embedded in longer Go literals.
func catalogCopyWindows(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	add := func(text, source string) {
		text = normalizeCopyForWindow(text)
		if len(text) < catalogCopyWindow {
			return
		}
		key := text[:catalogCopyWindow]
		if _, seen := out[key]; !seen {
			out[key] = source
		}
	}

	packs := filepath.Join(root, "lycaon", "config", "packs")
	err := filepath.WalkDir(packs, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		rel := filepath.ToSlash(mustRel(t, root, path))
		switch {
		case strings.HasSuffix(path, ".md"):
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fenced := false
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "```") {
					fenced = !fenced
					continue
				}
				// Fenced examples and tables share format boilerplate with code.
				if fenced || line == "" || strings.HasPrefix(line, "|") {
					continue
				}
				add(line, rel)
			}
		case strings.HasSuffix(path, ".yaml") && strings.Contains(rel, "/policy/"):
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var unit map[string]any
			if err := yaml.Unmarshal(data, &unit); err != nil {
				return nil // Catalog validation reports malformed YAML.
			}
			for _, field := range catalogCopyFields(unit) {
				add(field, rel)
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk config packs for catalog copy", err)
	return out
}

// Policy metadata and nested copy both contain presentation text.
func catalogCopyFields(unit map[string]any) []string {
	keys := []string{"what", "cause", "why", "fix", "instead", "message", "title"}
	var out []string
	collect := func(m map[string]any) {
		for _, key := range keys {
			if value, ok := m[key].(string); ok {
				out = append(out, value)
			}
		}
	}
	collect(unit)
	if nested, ok := unit["copy"].(map[string]any); ok {
		collect(nested)
	}
	return out
}

func matchCatalogCopy(text string, windows map[string]string) (string, bool) {
	normalized := normalizeCopyForWindow(text)
	for i := 0; i+catalogCopyWindow <= len(normalized); i++ {
		if source, ok := windows[normalized[i:i+catalogCopyWindow]]; ok {
			return source, true
		}
	}
	return "", false
}

// Format verbs and template tags can interrupt otherwise identical copy.
var copyPlaceholderRE = regexp.MustCompile(`%[+\-# 0-9.]*[a-zA-Z]|\{\{.*?\}\}|\{%.*?%\}`)

func normalizeCopyForWindow(text string) string {
	return strings.Join(strings.Fields(copyPlaceholderRE.ReplaceAllString(text, " ")), " ")
}
