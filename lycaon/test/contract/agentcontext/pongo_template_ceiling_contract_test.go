package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Pongo compilation and execution stay inside internal/pongoplain.
func TestPongoTemplateSetsCarryTheHostCeiling(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	ceiling := filepath.Join("lycaon", "internal", "pongoplain")
	err := filepath.WalkDir(filepath.Join(root, "lycaon"), func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		display, relErr := filepath.Rel(root, path)
		if relErr != nil {
			display = path
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		insideCeiling := strings.HasPrefix(display, ceiling+string(filepath.Separator))
		for _, finding := range scanPongoCeiling(fset, f, display, insideCeiling) {
			t.Error(finding)
		}
		return nil
	})
	testutil.FailErr(t, "walk lycaon", err)
}

// pongoTemplateMethods is the complete exported method set of *pongo2.Template.
// Each one renders, so each one must go through pongoplain.Execute.
// TestPongoExecutorSetMatchesTemplateAPI keeps it complete.
var pongoTemplateMethods = map[string]bool{
	"Execute":                 true,
	"ExecuteBlocks":           true,
	"ExecuteBytes":            true,
	"ExecuteWriter":           true,
	"ExecuteWriterUnbuffered": true,
}

// pongoSetCeilingMutators re-open a template set after pongoplain configured it.
// Banning a tag is how the ceiling is built; un-banning one elsewhere removes it.
var pongoSetCeilingMutators = map[string]bool{
	"AddLoader":  true,
	"BanFilter":  true,
	"BanTag":     true,
	"CleanCache": true,
}

// importedAs returns the local package identifier.
func importedAs(file *ast.File, pkgPath, defaultName string) (string, bool) {
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != pkgPath {
			continue
		}
		if spec.Name != nil {
			if spec.Name.Name == "_" {
				return "", false
			}
			return spec.Name.Name, true
		}
		return defaultName, true
	}
	return "", false
}

// scanPongoCeiling flags pongo2 use outside the ceiling. The rule names
// the package rather than its entry points, so an upstream release cannot add
// one this contract has never heard of. A type conversion such as
// pongo2.Context(m) parses as a call and is flagged too.
func scanPongoCeiling(fset *token.FileSet, file *ast.File, display string, insideCeiling bool) []string {
	pongoIdent, hasPongo := importedAs(file, "github.com/flosch/pongo2/v6", "pongo2")
	plainIdent, hasPlain := importedAs(file, "github.com/lycaon/lycaon/internal/pongoplain", "pongoplain")
	if !hasPongo && !hasPlain {
		return nil
	}
	var findings []string
	if hasPongo && pongoIdent == "." && !insideCeiling {
		findings = append(findings, display+": pongo2 dot imports bypass the template ceiling")
	}
	if hasPlain && plainIdent == "." {
		findings = append(findings, display+": pongoplain dot imports bypass the template ceiling scan")
	}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if hasPongo && !insideCeiling && pongoTemplateMethods[sel.Sel.Name] {
			if pkg, ok := sel.X.(*ast.Ident); ok && hasPlain && pkg.Name == plainIdent {
				return true
			}
			findings = append(findings, display+":"+strconv.Itoa(fset.Position(call.Pos()).Line)+
				": pongo2 template execution bypasses internal/pongoplain resource and context ceilings — "+
				"use pongoplain.Execute")
			return true
		}
		if hasPongo && !insideCeiling && pongoSetCeilingMutators[sel.Sel.Name] {
			findings = append(findings, display+":"+strconv.Itoa(fset.Position(call.Pos()).Line)+
				": ."+sel.Sel.Name+" re-opens a template set after internal/pongoplain configured "+
				"its ceiling — the ban list is the ceiling, so it is built in one place only")
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch {
		case hasPongo && pkg.Name == pongoIdent:
			if insideCeiling {
				return true
			}
			findings = append(findings, display+":"+strconv.Itoa(fset.Position(call.Pos()).Line)+
				": pongo2."+sel.Sel.Name+" reaches the template package outside internal/pongoplain, "+
				"so it carries no tag ceiling — use pongoplain.NewSet or pongoplain.Compile")
		case hasPlain && pkg.Name == plainIdent:
			if sel.Sel.Name != "Compile" || len(call.Args) != 1 {
				return true
			}
			if !isAssembledTemplateSource(call.Args[0]) {
				return true
			}
			findings = append(findings, display+":"+strconv.Itoa(fset.Position(call.Pos()).Line)+
				": pongoplain.Compile is handed assembled template source; "+
				"interpolating authored text into template syntax hands the author the tags around it — "+
				"pass the authored body and put the branch in Go")
		}
		return true
	})
	return findings
}

// TestPongoExecutorSetMatchesTemplateAPI derives the exported method set of
// *pongo2.Template from the module source and requires pongoTemplateMethods to
// match it exactly, so an upstream render method cannot go unwatched.
func TestPongoExecutorSetMatchesTemplateAPI(t *testing.T) {
	dir := pongoModuleDir(t)
	entries, err := os.ReadDir(dir)
	testutil.FailErr(t, "read pongo2 module dir", err)

	live := map[string]bool{}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 || !fn.Name.IsExported() {
				continue
			}
			if receiverTypeName(fn.Recv.List[0].Type) == "Template" {
				live[fn.Name.Name] = true
			}
		}
	}
	if len(live) == 0 {
		t.Fatalf("found no exported *pongo2.Template methods under %s — the derivation is "+
			"broken, and this contract would accept any executor list", dir)
	}

	onlyLive, onlyListed := testutil.SetDiff(sortedKeys(live), sortedKeys(pongoTemplateMethods))
	if len(onlyLive) > 0 {
		t.Errorf("pongo2 exposes template render methods pongoTemplateMethods does not watch: %v\n"+
			"Each renders outside internal/pongoplain unless this contract knows about it.", onlyLive)
	}
	if len(onlyListed) > 0 {
		t.Errorf("pongoTemplateMethods names methods *pongo2.Template no longer has: %v\n"+
			"A stale name matches nothing and hides that the real one went unwatched.", onlyListed)
	}
}

// pongoModuleDir locates the pongo2 source in the module cache, skipping when it
// has not been downloaded.
func pongoModuleDir(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/flosch/pongo2/v6").Output()
	if err != nil {
		t.Skipf("pongo2 module not resolvable (run `go mod download`): %v", err)
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		t.Skip("pongo2 module has no local directory")
	}
	return dir
}

// Synthetic violations prove each scanner branch.
func TestPongoCeilingRuleFiresOnSyntheticViolations(t *testing.T) {
	const pongoImport = "import pongo2 \"github.com/flosch/pongo2/v6\"\n"
	const aliasImport = "import p2 \"github.com/flosch/pongo2/v6\"\n"
	const dotImport = "import . \"github.com/flosch/pongo2/v6\"\n"
	const plainImport = "import (\n\"fmt\"\n\"github.com/lycaon/lycaon/internal/pongoplain\"\n)\n"
	const mixedImport = "import (\n\"github.com/flosch/pongo2/v6\"\n\"github.com/lycaon/lycaon/internal/pongoplain\"\n)\n"

	for name, tc := range map[string]struct {
		src           string
		insideCeiling bool
		want          string
	}{
		"set built outside pongoplain": {
			src:  "package p\n" + pongoImport + "func f(l any) { set := pongo2.NewSet(\"x\", l); _ = set }\n",
			want: "carries no tag ceiling",
		},
		"parsed on the default set": {
			src:  "package p\n" + pongoImport + "func f(b string) { _, _ = pongo2.FromString(b) }\n",
			want: "carries no tag ceiling",
		},
		"autoescape reset elsewhere": {
			src:  "package p\n" + pongoImport + "func f() { pongo2.SetAutoescape(true) }\n",
			want: "carries no tag ceiling",
		},
		"rendered through a default-set shortcut": {
			src:  "package p\n" + pongoImport + "func f(b string) { _, _ = pongo2.RenderTemplateString(b, nil) }\n",
			want: "carries no tag ceiling",
		},
		"template executed directly": {
			src:  "package p\n" + pongoImport + "func f(t *pongo2.Template) { _, _ = t.Execute(nil) }\n",
			want: "bypasses internal/pongoplain resource",
		},
		// Rendering, registration twins, loader constructors, and re-opening a
		// configured ban list each reach the ceiling by a different route.
		"blocks executed directly": {
			src:  "package p\n" + pongoImport + "func f(t *pongo2.Template) { _, _ = t.ExecuteBlocks(nil, nil) }\n",
			want: "bypasses internal/pongoplain resource",
		},
		"tag replaced rather than registered": {
			src:  "package p\n" + pongoImport + "func f() { _ = pongo2.ReplaceTag(\"include\", nil) }\n",
			want: "carries no tag ceiling",
		},
		"loader constructed outside the ceiling": {
			src:  "package p\n" + pongoImport + "func f() { _ = pongo2.MustNewLocalFileSystemLoader(\"/tmp\") }\n",
			want: "carries no tag ceiling",
		},
		"ceiling re-opened on a configured set": {
			src:  "package p\n" + pongoImport + "func f(set *pongo2.TemplateSet) { set.BanTag(\"include\") }\n",
			want: "re-opens a template set",
		},
		"set built through an aliased import": {
			src:  "package p\n" + aliasImport + "func f(l any) { set := p2.NewSet(\"x\", l); _ = set }\n",
			want: "carries no tag ceiling",
		},
		"dot import": {
			src:  "package p\n" + dotImport + "func f(l any) { set := NewSet(\"x\", l); _ = set }\n",
			want: "dot imports bypass",
		},
		"template source concatenated": {
			src:  "package p\n" + plainImport + "func f(when string) { _, _ = pongoplain.Compile(\"{% if \" + when + \" %}1{% endif %}\") }\n",
			want: "assembled template source",
		},
		"template source formatted": {
			src:  "package p\n" + plainImport + "func f(when string) { _, _ = pongoplain.Compile(fmt.Sprintf(\"{%% if %s %%}1{%% endif %%}\", when)) }\n",
			want: "assembled template source",
		},
	} {
		t.Run(name, func(t *testing.T) {
			findings := scanSyntheticSource(t, tc.src, tc.insideCeiling)
			if len(findings) != 1 || !strings.Contains(findings[0], tc.want) {
				t.Fatalf("want one finding containing %q, got %v", tc.want, findings)
			}
		})
	}

	t.Run("the ceiling may build sets", func(t *testing.T) {
		src := "package pongoplain\n" + pongoImport + "func f(l any) { set := pongo2.NewSet(\"x\", l); _ = set }\n"
		if findings := scanSyntheticSource(t, src, true); len(findings) != 0 {
			t.Fatalf("flagged the defining package: %v", findings)
		}
	})

	t.Run("passing an authored body through is clean", func(t *testing.T) {
		src := "package p\n" + plainImport + "func f(body string) { _, _ = pongoplain.Compile(body) }\n"
		if findings := scanSyntheticSource(t, src, false); len(findings) != 0 {
			t.Fatalf("flagged a pass-through: %v", findings)
		}
	})

	t.Run("bounded execution in a file carrying pongo types is clean", func(t *testing.T) {
		src := "package p\n" + mixedImport + "func f(t *pongo2.Template) { _, _ = pongoplain.Execute(nil, t, nil) }\n"
		if findings := scanSyntheticSource(t, src, false); len(findings) != 0 {
			t.Fatalf("flagged centralized execution: %v", findings)
		}
	})

	t.Run("methods on a pongoplain-built set are clean", func(t *testing.T) {
		src := "package p\n" + pongoImport + "func f(set *pongo2.TemplateSet, ref string) { _, _ = set.FromCache(ref) }\n"
		if findings := scanSyntheticSource(t, src, false); len(findings) != 0 {
			t.Fatalf("flagged a method on an ceiling-managed set: %v", findings)
		}
	})
}

func scanSyntheticSource(t *testing.T, src string, insideCeiling bool) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "synthetic.go", src, 0)
	testutil.FailErr(t, "parse synthetic source", err)
	return scanPongoCeiling(fset, f, "synthetic.go", insideCeiling)
}

// isAssembledTemplateSource reports argument shapes that build template source
// rather than pass one through: string concatenation, and fmt.Sprintf.
func isAssembledTemplateSource(arg ast.Expr) bool {
	switch v := arg.(type) {
	case *ast.BinaryExpr:
		return v.Op == token.ADD
	case *ast.CallExpr:
		sel, ok := v.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return false
		}
		return pkg.Name == "fmt" && strings.HasPrefix(sel.Sel.Name, "Sprint")
	default:
		return false
	}
}
