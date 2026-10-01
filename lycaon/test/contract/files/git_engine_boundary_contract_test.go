package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/usernotice"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Neutralization keys correspond to gitexec.neutralizePrefixKeys.
var gitBoundaryNeutralizeKeys = []string{
	"core.hooksPath",
	"core.fsmonitor",
	"core.pager",
	"core.editor",
	"core.askpass",
	"core.attributesFile",
	"core.excludesFile",
	"protocol.ext.allow",
	"protocol.file.allow",
	"commit.gpgSign",
	"tag.gpgSign",
	"log.showSignature",
	"merge.verifySignatures",
	"advice.detachedHead",
}

// Diff flags disable drivers selected by repository attributes.
var gitBoundaryDiffDriverFlags = []string{"--no-ext-diff", "--no-textconv"}

// These config families lack flags that disable repository-defined programs.
var gitBoundaryRefusedConfigFamilies = []string{"filter", "merge"}

// TestGitBoundaryEngine checks the complete confined Git boundary.
func TestGitBoundaryEngine(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	assertGitBoundaryNoPathResolution(t, root)
	assertGitBoundaryNoConfigWrites(t, root)
	assertGitBoundaryNeutralizePrefixIntact(t, root)
	assertGitBoundaryHooksOffNotOptional(t, root)
	assertGitBoundaryPinFailsClosed(t, root)
	assertGitBoundaryWriteSinksWired(t, root)
	assertGitBoundaryNoticeUnits(t, root)
}

func assertGitBoundaryNoPathResolution(t *testing.T, root string) {
	t.Helper()
	var findings []string
	internalRoot := filepath.Join(root, "lycaon", "internal")
	fset := token.NewFileSet()
	err := filepath.WalkDir(internalRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if isLookPathGitOrLFS(call) {
				findings = append(findings, fset.Position(n.Pos()).String()+`: LookPath(git|git-lfs)`)
			}
			return true
		})
		return nil
	})
	contractcheck.FailErr(t, "walk internal", err)

	lycaonRoot := filepath.Join(root, "lycaon")
	err = filepath.WalkDir(lycaonRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(src), "/usr/bin/git") {
			findings = append(findings, rel+`: literal /usr/bin/git`)
		}
		return nil
	})
	contractcheck.FailErr(t, "walk lycaon", err)

	if len(findings) > 0 {
		t.Fatalf("PATH/system git resolution still present:\n  %s", strings.Join(findings, "\n  "))
	}
}

func isLookPathGitOrLFS(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "LookPath" || len(call.Args) < 1 {
		return false
	}
	return stringLitIs(call.Args[0], "git") || stringLitIs(call.Args[0], "git-lfs")
}

func assertGitBoundaryNoConfigWrites(t *testing.T, root string) {
	t.Helper()
	// The boundary scanner accounts for urlmatchQueryArgv.
	internalRoot := filepath.Join(root, "lycaon", "internal")
	fset := token.NewFileSet()
	var findings []string
	err := filepath.WalkDir(internalRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}
		inGitexec := strings.Contains(filepath.ToSlash(rel), "/internal/gitexec/")
		findings = append(findings, scanGitOneDoor(fset, file, rel, inGitexec)...)
		return nil
	})
	contractcheck.FailErr(t, "walk for config writes", err)
	var configFindings []string
	for _, f := range findings {
		if strings.Contains(f, "config --local") || strings.Contains(f, "git config") {
			configFindings = append(configFindings, f)
		}
	}
	if len(configFindings) > 0 {
		t.Fatalf("git config write argv still present:\n  %s", strings.Join(configFindings, "\n  "))
	}
}

func assertGitBoundaryNeutralizePrefixIntact(t *testing.T, root string) {
	t.Helper()
	src := contractcheck.ReadRepoFile(t, root, "lycaon/internal/gitexec/neutralize.go")
	for _, key := range gitBoundaryNeutralizeKeys {
		needle := `"` + key + `"`
		if !strings.Contains(src, needle) {
			t.Fatalf("neutralizePrefixKeys missing %s — do not drop a neutralization key", key)
		}
	}
	// The emitted prefix includes hooksPath neutralization.
	if !strings.Contains(src, "core.hooksPath=") {
		t.Fatal("neutralizeArgs must emit core.hooksPath=")
	}
	for _, flag := range gitBoundaryDiffDriverFlags {
		if !strings.Contains(src, `"`+flag+`"`) {
			t.Fatalf("diffDriverFlags missing %s — the diff machinery would run a repository-declared program", flag)
		}
	}
	cfg := contractcheck.ReadRepoFile(t, root, "lycaon/internal/gitexec/repoconfig.go")
	for _, section := range gitBoundaryRefusedConfigFamilies {
		if !strings.Contains(cfg, `Section:     "`+section+`"`) && !strings.Contains(cfg, `Section:   "`+section+`"`) {
			t.Fatalf("executableSubsectionFamilies missing %q — a repository could declare an executable driver this host cannot disable", section)
		}
	}
}

func assertGitBoundaryHooksOffNotOptional(t *testing.T, root string) {
	t.Helper()
	fset := token.NewFileSet()
	functions := map[string]*ast.FuncDecl{}
	for _, name := range []string{"gitexec.go", "records.go"} {
		path := filepath.Join(root, "lycaon", "internal", "gitexec", name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		contractcheck.FailErr(t, "parse git execution boundary", err)
		for _, declaration := range file.Decls {
			if fn, ok := declaration.(*ast.FuncDecl); ok && fn.Recv == nil {
				functions[fn.Name.Name] = fn
			}
		}
	}
	for caller, required := range map[string][]string{
		"Run": {"run"}, "RunRecords": {"run"}, "run": {"buildArgv"},
		"buildArgv": {"neutralizeArgs", "lfsFilterArgs"},
	} {
		fn := functions[caller]
		if fn == nil || fn.Body == nil {
			t.Fatalf("gitexec.%s not found", caller)
		}
		for _, callee := range required {
			if !funcCallsIdent(fn, callee) {
				t.Errorf("gitexec.%s must call %s so both execution modes use argument hardening", caller, callee)
			}
		}
	}
}

func funcCallsIdent(fd *ast.FuncDecl, name string) bool {
	found := false
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if ok && id.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

func assertGitBoundaryPinFailsClosed(t *testing.T, root string) {
	t.Helper()
	resolveSrc := contractcheck.ReadRepoFile(t, root, "lycaon/internal/gitengine/resolve.go")
	if !strings.Contains(resolveSrc, `Reason:   "version_mismatch"`) &&
		!strings.Contains(resolveSrc, `Reason: "version_mismatch"`) {
		t.Fatal("assertPinnedVersion must return UnavailableError{Reason: version_mismatch}")
	}
	// Version mismatches stop engine resolution.
	engineDir := filepath.Join(root, "lycaon", "internal", "gitengine")
	err := filepath.WalkDir(engineDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(src)
		for _, bad := range []string{"slog.Warn", "log.Printf", "log.Println", "log.Warn"} {
			if strings.Contains(text, bad) && strings.Contains(strings.ToLower(text), "version") {
				t.Errorf("%s: %s near version handling — pin assertion must fail closed, not warn", path, bad)
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk gitengine", err)
}

func assertGitBoundaryWriteSinksWired(t *testing.T, root string) {
	t.Helper()
	resolveWrite := contractcheck.ReadRepoFile(t, root, "lycaon/internal/tools/projectpaths/projectpaths.go")
	if !strings.Contains(resolveWrite, "IsGitInternalsWritePath") {
		t.Fatal("ResolveWrite must call IsGitInternalsWritePath")
	}
	extract := contractcheck.ReadRepoFile(t, root, "lycaon/internal/tools/native/extract_archive.go")
	if !strings.Contains(extract, "IsGitInternalsWritePath") {
		t.Fatal("extract_archive per-entry guard must call IsGitInternalsWritePath")
	}
}

func assertGitBoundaryNoticeUnits(t *testing.T, root string) {
	t.Helper()
	cfg, err := usernotice.LoadNoticeDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "host", "user-notices"))
	contractcheck.FailErr(t, "load user-notices", err)
	for _, code := range []string{
		"GIT_ENGINE_UNAVAILABLE",
		"GIT_INTERNALS_WRITE_DENIED",
		"GIT_SIGNING_UNSUPPORTED",
		"GIT_REPO_CONFIG_UNSAFE",
	} {
		entry, ok := cfg.UserNotices[code]
		if !ok {
			t.Fatalf("missing user-notice unit %s", code)
		}
		if !entry.IsUserVisible() {
			t.Fatalf("%s must be user_visible", code)
		}
	}
	if _, ok := cfg.UserNotices["GIT_CLT_MISSING"]; ok {
		t.Fatal("GIT_CLT_MISSING notice unit must be absent")
	}
}
