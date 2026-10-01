package contract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// The condition vocabulary's shell operands are closed copies of shell unions.
// Each pair here ties one Go map in internal/contribution/vocabulary.go to the
// Den declaration the evaluator binds, so growing either side alone fails.
func TestContributionShellVocabulariesMatchDen(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	vocab := filepath.Join(root, "lycaon", "internal", "contribution", "vocabulary.go")
	den := filepath.Join(root, "lycaon-den")

	cases := []struct {
		goVar   string
		source  string
		extract func(content string) ([]string, error)
	}{
		{
			goVar:   "focusRegions",
			source:  filepath.Join(den, "src", "shortcuts", "focus-region.ts"),
			extract: func(c string) ([]string, error) { return tsConstStringArray(c, "FOCUS_REGION_IDS") },
		},
		{
			goVar:   "workspaceKinds",
			source:  filepath.Join(den, "src", "shortcuts", "peer-view-subject.ts"),
			extract: func(c string) ([]string, error) { return tsStringUnion(c, "WorkspaceKind") },
		},
		{
			goVar:   "contextFeatures",
			source:  filepath.Join(den, "shared", "app-state-types.ts"),
			extract: func(c string) ([]string, error) { return tsConstStringArray(c, "CONTEXT_NAV_CATALOG") },
		},
		{
			goVar:   "settingsSections",
			source:  filepath.Join(den, "src", "settings", "settings-nav-model.ts"),
			extract: func(c string) ([]string, error) { return tsStringUnion(c, "SettingsSection") },
		},
	}

	for _, tc := range cases {
		t.Run(tc.goVar, func(t *testing.T) {
			t.Parallel()
			goVals, err := goMapStringKeys(vocab, tc.goVar)
			contractcheck.FailErr(t, "read "+tc.goVar+" keys", err)
			data, err := os.ReadFile(tc.source)
			contractcheck.FailErr(t, "read "+tc.source, err)
			denVals, err := tc.extract(string(data))
			contractcheck.FailErr(t, "extract Den vocabulary for "+tc.goVar, err)
			contractcheck.FailSetEqual(t, tc.goVar+" Go vs Den", denVals, goVals)
		})
	}
}

// goMapStringKeys returns the string keys of a package-level map literal.
func goMapStringKeys(path, varName string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || value.Names[0].Name != varName || len(value.Values) != 1 {
				continue
			}
			lit, ok := value.Values[0].(*ast.CompositeLit)
			if !ok {
				return nil, fmt.Errorf("%s is not a composite literal", varName)
			}
			var keys []string
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					return nil, fmt.Errorf("%s has a non key-value element", varName)
				}
				basic, ok := kv.Key.(*ast.BasicLit)
				if !ok || basic.Kind != token.STRING {
					return nil, fmt.Errorf("%s has a non-string key", varName)
				}
				key, err := strconv.Unquote(basic.Value)
				if err != nil {
					return nil, err
				}
				keys = append(keys, key)
			}
			return keys, nil
		}
	}
	return nil, fmt.Errorf("var %s not found in %s", varName, path)
}

// tsConstStringArray extracts `export const NAME = ["a", "b"] as const` values.
func tsConstStringArray(content, name string) ([]string, error) {
	re := regexp.MustCompile(`(?s)export const ` + regexp.QuoteMeta(name) + `[^=]*=\s*\[(.*?)\]`)
	m := re.FindStringSubmatch(content)
	if m == nil {
		return nil, fmt.Errorf("const array %s not found", name)
	}
	values := regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1)
	if len(values) == 0 {
		return nil, fmt.Errorf("const array %s is empty", name)
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, v[1])
	}
	return out, nil
}

// tsStringUnion extracts `export type Name = "a" | "b"` values across lines.
func tsStringUnion(content, name string) ([]string, error) {
	re := regexp.MustCompile(`(?s)export type ` + regexp.QuoteMeta(name) + `\s*=\s*(.*?);`)
	m := re.FindStringSubmatch(content)
	if m == nil {
		return nil, fmt.Errorf("type union %s not found", name)
	}
	var out []string
	for _, seg := range strings.Split(m[1], "|") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		if !strings.HasPrefix(seg, `"`) || !strings.HasSuffix(seg, `"`) {
			return nil, fmt.Errorf("type %s has a non-literal member %q", name, seg)
		}
		out = append(out, strings.Trim(seg, `"`))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("type union %s is empty", name)
	}
	return out, nil
}
