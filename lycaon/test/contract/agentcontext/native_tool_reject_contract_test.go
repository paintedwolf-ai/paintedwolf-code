package contract

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

func securityNativeRuntime(t *testing.T) *toolhost.Runtime {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	configRoot := filepath.Join(filepath.Dir(file), "..", "..", "..")
	rt, err := toolhost.NewRuntime(toolhost.RuntimeConfig{ConfigRoot: configRoot, Catalog: contractcheck.StockCatalog(t)})
	testutil.FailErr(t, "toolhost.NewRuntime", err)
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfig", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	rt.Authority.ApplyGuidanceRejects(guidance.NewStaticRejectFormatter(hints))
	toolfixture.WireContractBlockPlane(t, rt, guidance.NewStaticRejectFormatter(hints))
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(configRoot, "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "LoadSchemaDir", err)
	rt.Executor.Metadata.SetToolSchemas(schemas)
	return rt
}

func assertStructuredRejectCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected structured reject %q", code)
	}
	reject, ok := guidance.RefusalFromError(err)
	if !ok || reject.Code() != code {
		t.Fatalf("expected typed reject %q, got %v", code, err)
	}
}

func TestNativeFindPathEscapeE2E(t *testing.T) {
	t.Parallel()
	b := contractcheck.ProdToolBoundary(t)
	find := &surveytools.FindTool{Boundary: b}
	_, err := find.Run(context.Background(), map[string]any{"path": "../../etc/passwd"}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	var reject *toolrejection.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "SURVEY_PATH_ESCAPE" {
		t.Fatalf("err = %v want SURVEY_PATH_ESCAPE", err)
	}
}

func TestNativeListDirControlPlaneE2E(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	b := contractcheck.ProdToolBoundary(t)
	listDir := &surveytools.ListDirTool{Boundary: b}
	_, err := listDir.Run(context.Background(), map[string]any{"path": filepath.Join(cfg, "debug", "sessions")}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	var reject *toolrejection.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "SANDBOX_CONTROL_PLANE_DENIED" {
		t.Fatalf("err = %v want SANDBOX_CONTROL_PLANE_DENIED", err)
	}
}

func TestNativeListDirScopeFieldsRemainStructuredE2E(t *testing.T) {
	t.Parallel()
	b := contractcheck.ProdToolBoundary(t)
	root := t.TempDir()
	const path = ".include_hidden=false,max_depth=2"
	if err := os.Mkdir(filepath.Join(root, path), 0o755); err != nil {
		contractcheck.FailErr(t, "create literal scope-like path", err)
	}
	if err := os.WriteFile(filepath.Join(root, path, "marker.txt"), []byte("marker"), 0o600); err != nil {
		contractcheck.FailErr(t, "create scoped marker", err)
	}
	listDir := &surveytools.ListDirTool{Boundary: b}
	out, err := listDir.Run(context.Background(), map[string]any{
		"path": path, "max_depth": 1,
	}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	contractcheck.FailErr(t, "list literal scope-like path", err)
	if !strings.Contains(out, "marker.txt") {
		t.Fatalf("literal directory was not listed: %s", out)
	}
}

func TestNativeGrepUnsupportedRegexStructuredRejectE2E(t *testing.T) {
	program := toolfixture.NewProgramFixture(t)
	outcome, err := program.Invoke(t, "rg -P '(?R)' .", tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
			ActiveRootID:       "r1",
			RepoFileCount:      100,
			RepoFileCountKnown: true},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	assertProgramRefusal(t, outcome, err, "GREP_REGEX_UNSUPPORTED")
}

func TestNativeSummarizeNoInputStructuredRejectE2E(t *testing.T) {
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfig", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	formatter := guidance.NewStaticRejectFormatter(hints)

	formatted := toolrejection.FormatDecisionReject("SUMMARIZE_NO_INPUT", map[string]any{"need_one_of": []string{"path", "paths", "pattern", "content"}}, formatter)
	assertStructuredRejectCode(t, formatted, "SUMMARIZE_NO_INPUT")
	if !strings.Contains(formatted.Error(), "sibling") && !strings.Contains(formatted.Error(), "path") {
		t.Fatalf("expected path/sibling fix guidance, got %q", formatted.Error())
	}
}

func TestNativeChmod777StructuredRejectE2E(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		testutil.FailErr(t, "write script", err)
	}
	program := toolfixture.NewProgramFixture(t)
	outcome, err := program.Invoke(t, "chmod 777 -- run.sh", tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	assertProgramRefusal(t, outcome, err, "CHMOD_MODE_DENIED")
}

func TestNativeChmodPlusXOnScriptSuccessE2E(t *testing.T) {
	root := t.TempDir()
	scriptsDir := filepath.Join(root, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir scripts", err)
	}
	scriptPath := filepath.Join(scriptsDir, "foo.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\n"), 0o644); err != nil {
		testutil.FailErr(t, "write script", err)
	}
	program := toolfixture.NewProgramFixture(t)
	outcome, err := program.Invoke(t, "chmod +x -- scripts/foo.sh", tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	testutil.FailErr(t, "chmod +x", err)
	if outcome.Status != 0 || outcome.Refusal != nil {
		t.Fatalf("chmod outcome = %+v", outcome)
	}
	info, err := os.Stat(scriptPath)
	testutil.FailErr(t, "stat script", err)
	if info.Mode()&0o111 == 0 {
		t.Fatal("expected executable bit after chmod +x")
	}
}

func TestNativeDeleteNonEmptyDirPreservesContentsE2E(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "pkg")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.go"), []byte("package pkg\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	program := toolfixture.NewProgramFixture(t)
	outcome, err := program.Invoke(t, "rm -- pkg", tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	testutil.FailErr(t, "run nonrecursive directory removal", err)
	if outcome.Status == 0 || outcome.Refusal != nil {
		t.Fatalf("nonrecursive removal outcome=%+v", outcome)
	}
	data, err := os.ReadFile(filepath.Join(dir, "keep.go"))
	testutil.FailErr(t, "retained directory content", err)
	if string(data) != "package pkg\n" {
		t.Fatal("failed removal changed directory content")
	}
}

func TestNativeDeleteOutsideWriteScopeStructuredRejectE2E(t *testing.T) {
	root := t.TempDir()
	program := toolfixture.NewProgramFixture(t)
	outcome, err := program.Invoke(t, "rm -- ../outside.txt", tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	assertProgramRefusal(t, outcome, err, "DELETE_PATH_DENIED")
}

func TestNativeDeleteGitPathStructuredRejectE2E(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir .git", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		testutil.FailErr(t, "write HEAD", err)
	}
	program := toolfixture.NewProgramFixture(t)
	outcome, err := program.Invoke(t, "rm -- .git/HEAD", tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	assertProgramRefusal(t, outcome, err, "GIT_INTERNALS_WRITE_DENIED")
}

func TestNativeCopySizeExceededStructuredRejectE2E(t *testing.T) {
	root := t.TempDir()
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	testutil.FailErr(t, "read native copy cap", err)
	var maximum int64
	for _, limit := range catalog.Language.Limits {
		if limit.ID == "recursive_mutation_bytes" {
			maximum = limit.Maximum
		}
	}
	if maximum <= 0 {
		t.Fatal("copy byte cap missing")
	}
	file, err := os.Create(filepath.Join(root, "big.txt"))
	testutil.FailErr(t, "create sparse oversized copy source", err)
	testutil.FailErr(t, "size sparse source above catalog bound", file.Truncate(maximum+1))
	testutil.FailErr(t, "close sparse source", file.Close())
	program := toolfixture.NewProgramFixture(t)
	outcome, err := program.Invoke(t, "cp -- big.txt copy.txt", tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	assertProgramRefusal(t, outcome, err, "COPY_SIZE_EXCEEDED")
}

func TestNativeMoveOverwriteSuccessE2E(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "src.txt"), []byte("new"), 0o644); err != nil {
		testutil.FailErr(t, "write src", err)
	}
	if err := os.WriteFile(filepath.Join(root, "dst.txt"), []byte("old"), 0o644); err != nil {
		testutil.FailErr(t, "write dst", err)
	}
	program := toolfixture.NewProgramFixture(t)
	outcome, err := program.Invoke(t, "mv -- src.txt dst.txt", tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	testutil.FailErr(t, "move overwrite", err)
	if outcome.Status != 0 || outcome.Refusal != nil {
		t.Fatalf("move outcome=%+v", outcome)
	}
	data, err := os.ReadFile(filepath.Join(root, "dst.txt"))
	testutil.FailErr(t, "read dst", err)
	if string(data) != "new" {
		t.Fatalf("dst = %q", data)
	}
}

func TestNativeMkdirFileExistsPreservesContentsE2E(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	program := toolfixture.NewProgramFixture(t)
	outcome, err := program.Invoke(t, "mkdir -- file.txt", tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	testutil.FailErr(t, "run existing-file mkdir", err)
	if outcome.Status == 0 || outcome.Refusal != nil {
		t.Fatalf("mkdir outcome=%+v", outcome)
	}
	data, err := os.ReadFile(filepath.Join(root, "file.txt"))
	testutil.FailErr(t, "read preserved file", err)
	if string(data) != "x" {
		t.Fatal("mkdir changed existing file")
	}
}

func TestNativeChownForeignUIDStructuredRejectE2E(t *testing.T) {
	if os.Getuid() == 65534 {
		t.Skip("effective uid is nobody")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	program := toolfixture.NewProgramFixture(t)
	outcome, err := program.Invoke(t, "chown 65534 -- a.txt", tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	assertProgramRefusal(t, outcome, err, "CHOWN_TARGET_DENIED")
}

func TestNativeChownGitPathStructuredRejectE2E(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir .git", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		testutil.FailErr(t, "write HEAD", err)
	}
	program := toolfixture.NewProgramFixture(t)
	outcome, err := program.Invoke(t, fmt.Sprintf("chown %d -- .git/HEAD", os.Getuid()), tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	assertProgramRefusal(t, outcome, err, "GIT_INTERNALS_WRITE_DENIED")
}

func TestNativeReadRejectsDirectoryE2E(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "src"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	b := contractcheck.ProdToolBoundary(t)
	read := &surveytools.ReadTool{Boundary: b}
	_, err := read.Run(context.Background(), map[string]any{"path": "src"}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "explore_readonly"},
	})
	var reject *toolrejection.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "READ_IS_DIRECTORY" {
		t.Fatalf("read on directory err = %v want READ_IS_DIRECTORY", err)
	}
}

func assertProgramRefusal(t *testing.T, outcome command.ProgramOutcome, err error, code string) {
	t.Helper()
	if err != nil {
		assertStructuredRejectCode(t, err, code)
		return
	}
	if outcome.Refusal == nil || outcome.Refusal.Code != code || outcome.Status == 0 {
		t.Fatalf("expected typed program refusal %s, got %+v", code, outcome)
	}
}
