package contract

import (
	"archive/zip"
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/usernotice"
	"github.com/lycaon/lycaon/pkg/api"
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
	assertGitBoundaryWriteSinksWired(t)
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
	neutralize := contractcheck.ParseRepoGoFile(t, root, "lycaon/internal/gitexec/neutralize.go")
	keys := contractcheck.StringLiterals(contractcheck.PackageVar(t, neutralize, "neutralizePrefixKeys"))
	for _, key := range gitBoundaryNeutralizeKeys {
		if !keys[key] {
			t.Fatalf("neutralizePrefixKeys missing %s — do not drop a neutralization key", key)
		}
	}
	// The emitted prefix includes hooksPath neutralization.
	if !contractcheck.StringLiterals(contractcheck.TopLevelFunc(t, neutralize, "neutralizeArgs"))["core.hooksPath="] {
		t.Fatal("neutralizeArgs must emit core.hooksPath=")
	}
	flags := contractcheck.StringLiterals(contractcheck.PackageVar(t, neutralize, "diffDriverFlags"))
	for _, flag := range gitBoundaryDiffDriverFlags {
		if !flags[flag] {
			t.Fatalf("diffDriverFlags missing %s — the diff machinery would run a repository-declared program", flag)
		}
	}
	config := contractcheck.ParseRepoGoFile(t, root, "lycaon/internal/gitexec/repoconfig.go")
	families := contractcheck.KeyedStrings(contractcheck.PackageVar(t, config, "executableSubsectionFamilies"), "Section")
	for _, section := range gitBoundaryRefusedConfigFamilies {
		if !families[section] {
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
	resolve := contractcheck.ParseRepoGoFile(t, root, "lycaon/internal/gitengine/resolve.go")
	if !contractcheck.KeyedStrings(resolve, "Reason")["version_mismatch"] {
		t.Fatal("assertPinnedVersion must return UnavailableError{Reason: version_mismatch}")
	}
	// Version mismatches stop engine resolution, so the package logs no warning it could continue past.
	engineDir := filepath.Join(root, "lycaon", "internal", "gitengine")
	_, files := contractcheck.ParseNonTestGoTree(t, engineDir)
	for _, file := range files {
		for _, call := range [][2]string{{"slog", "Warn"}, {"log", "Printf"}, {"log", "Println"}, {"log", "Warn"}} {
			if contractcheck.CallsSelector(file, call[0], call[1]) {
				t.Errorf("%s calls %s.%s — pin assertion must fail closed, not warn", file.Name.Name, call[0], call[1])
			}
		}
	}
}

// assertGitBoundaryWriteSinksWired drives each write sink. The class distinguishes the Git write guard
// from the broader metadata floor beneath it, which would refuse these paths without naming one.
func assertGitBoundaryWriteSinksWired(t *testing.T) {
	t.Helper()
	project := t.TempDir()
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: "project", SessionID: "session", Agent: toolprofiles.DefaultToolProfileID},
		Source: tools.InvocationSource{
			Roots:               []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}},
			ActiveRootID:        "root",
			SourceWorkspaceKind: api.SourceWorkspaceKindProject,
		},
	}
	for path, class := range map[string]string{".git/config": "config", ".git/hooks/pre-commit": "hooks"} {
		_, err := projectpaths.ResolveWrite(t.Context(), nil, tctx, path)
		assertGitInternalsDenied(t, "ResolveWrite "+path, err, class)
	}

	var archive bytes.Buffer
	zipped := zip.NewWriter(&archive)
	entry, err := zipped.Create(".git/hooks/pre-commit")
	contractcheck.FailErr(t, "zip entry", err)
	_, err = entry.Write([]byte("#!/bin/sh\n"))
	contractcheck.FailErr(t, "zip content", err)
	contractcheck.FailErr(t, "zip close", zipped.Close())
	contractcheck.FailErr(t, "write archive", os.WriteFile(filepath.Join(project, "a.zip"), archive.Bytes(), 0o644))
	tool := &native.ExtractArchiveTool{Boundary: contractcheck.ProdToolBoundary(t)}
	_, err = tool.Run(t.Context(), map[string]any{"path": "a.zip", "dest": "out"}, tctx)
	assertGitInternalsDenied(t, "extract_archive", err, "hooks")
	if _, err := os.Lstat(filepath.Join(project, "out", ".git", "hooks", "pre-commit")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused archive entry reached the project: %v", err)
	}
}

func assertGitInternalsDenied(t *testing.T, sink string, err error, class string) {
	t.Helper()
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "GIT_INTERNALS_WRITE_DENIED" || reject.Data["class"] != class {
		t.Fatalf("%s = %v, want GIT_INTERNALS_WRITE_DENIED of class %s", sink, err, class)
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
