package contract

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

func securityNativeExecutor(t *testing.T) *toolexecution.Executor {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	configRoot := filepath.Join(filepath.Dir(file), "..", "..", "..")
	rt, err := toolhost.NewRuntime(toolhost.RuntimeConfig{ConfigRoot: configRoot, Catalog: contractcheck.StockCatalog(t)})
	testutil.FailErr(t, "toolhost.NewRuntime", err)
	testutil.FailErr(t, "register http_request", httpaction.Register(rt.Registry, httpaction.Deps{Boundary: rt.Boundary}))
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfig", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	rt.Authority.ApplyGuidanceRejects(guidance.NewStaticRejectFormatter(hints))
	toolfixture.WireContractBlockPlane(t, rt, guidance.NewStaticRejectFormatter(hints))
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(configRoot, "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "LoadSchemaDir", err)
	rt.Executor.Metadata.SetToolSchemas(schemas)
	return rt.Executor
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

func TestNativeGrepInvalidRegexStructuredRejectE2E(t *testing.T) {
	exec := securityNativeExecutor(t)
	_, err := exec.Invoke(context.Background(), "grep", map[string]any{
		"pattern": "[unclosed",
	}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
			ActiveRootID:       "r1",
			RepoFileCount:      100,
			RepoFileCountKnown: true},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	assertStructuredRejectCode(t, err, "GREP_REGEX_INVALID")
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
	exec := securityNativeExecutor(t)
	_, err := exec.Invoke(context.Background(), "chmod", map[string]any{
		"paths": []any{"run.sh"},
		"mode":  "777",
	}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	assertStructuredRejectCode(t, err, "CHMOD_MODE_DENIED")
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
	exec := securityNativeExecutor(t)
	out, err := exec.Invoke(context.Background(), "chmod", map[string]any{
		"paths": []any{"scripts/foo.sh"},
		"mode":  "+x",
	}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	testutil.FailErr(t, "chmod +x", err)
	if !strings.Contains(out, "mode_after") {
		t.Fatalf("chmod output = %q", out)
	}
	info, err := os.Stat(scriptPath)
	testutil.FailErr(t, "stat script", err)
	if info.Mode()&0o111 == 0 {
		t.Fatal("expected executable bit after chmod +x")
	}
}

func TestNativeDeleteNonEmptyDirStructuredRejectE2E(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "pkg")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.go"), []byte("package pkg\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	exec := securityNativeExecutor(t)
	_, err := exec.Invoke(context.Background(), "delete", map[string]any{
		"paths": []any{"pkg"},
	}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	assertStructuredRejectCode(t, err, "DELETE_NOT_EMPTY")
}

func TestNativeDeleteOutsideWriteScopeStructuredRejectE2E(t *testing.T) {
	root := t.TempDir()
	exec := securityNativeExecutor(t)
	_, err := exec.Invoke(context.Background(), "delete", map[string]any{
		"paths": []any{"../outside.txt"},
	}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	if err == nil {
		t.Fatal("expected delete path denial")
	}
	msg := err.Error()
	if !strings.Contains(msg, "DELETE_PATH_DENIED") && !strings.Contains(msg, "path escape") {
		t.Fatalf("error = %q want DELETE_PATH_DENIED or path escape", msg)
	}
}

func TestNativeDeleteGitPathStructuredRejectE2E(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir .git", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		testutil.FailErr(t, "write HEAD", err)
	}
	exec := securityNativeExecutor(t)
	_, err := exec.Invoke(context.Background(), "delete", map[string]any{
		"paths": []any{".git/HEAD"},
	}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	assertStructuredRejectCode(t, err, "GIT_INTERNALS_WRITE_DENIED")
}

func TestNativeCopySizeExceededStructuredRejectE2E(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(strings.Repeat("x", 200)), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	exec := securityNativeExecutor(t)
	_, err := exec.Invoke(context.Background(), "copy", map[string]any{
		"copies": []any{
			map[string]any{"from": "big.txt", "to": "copy.txt"},
		},
		"max_file_bytes": 100,
	}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	assertStructuredRejectCode(t, err, "COPY_SIZE_EXCEEDED")
}

func TestNativeMoveOverwriteSuccessE2E(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "src.txt"), []byte("new"), 0o644); err != nil {
		testutil.FailErr(t, "write src", err)
	}
	if err := os.WriteFile(filepath.Join(root, "dst.txt"), []byte("old"), 0o644); err != nil {
		testutil.FailErr(t, "write dst", err)
	}
	exec := securityNativeExecutor(t)
	_, err := exec.Invoke(context.Background(), "move", map[string]any{
		"moves": []any{
			map[string]any{"from": "src.txt", "to": "dst.txt"},
		},
	}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	testutil.FailErr(t, "move overwrite", err)
	data, err := os.ReadFile(filepath.Join(root, "dst.txt"))
	testutil.FailErr(t, "read dst", err)
	if string(data) != "new" {
		t.Fatalf("dst = %q", data)
	}
}

func TestNativeMkdirFileExistsStructuredRejectE2E(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	exec := securityNativeExecutor(t)
	_, err := exec.Invoke(context.Background(), "mkdir", map[string]any{
		"paths": []any{"file.txt"},
	}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	assertStructuredRejectCode(t, err, "MKDIR_FILE_EXISTS")
}

func TestNativeCommandCpHabitRedirectMatcher(t *testing.T) {
	t.Parallel()
	exec := securityNativeExecutor(t)
	root := t.TempDir()
	_, err := exec.Invoke(context.Background(), "command", map[string]any{"command": "cp src.txt dst.txt"}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	assertStructuredRejectCode(t, err, "USE_COPY_NATIVE")
}

func TestNativeChownForeignUIDStructuredRejectE2E(t *testing.T) {
	if os.Getuid() == 65534 {
		t.Skip("effective uid is nobody")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	exec := securityNativeExecutor(t)
	_, err := exec.Invoke(context.Background(), "chown", map[string]any{
		"paths": []any{"a.txt"},
		"owner": "65534",
	}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	assertStructuredRejectCode(t, err, "CHOWN_TARGET_DENIED")
}

func TestNativeChownGitPathStructuredRejectE2E(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir .git", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		testutil.FailErr(t, "write HEAD", err)
	}
	exec := securityNativeExecutor(t)
	_, err := exec.Invoke(context.Background(), "chown", map[string]any{
		"paths": []any{".git/HEAD"},
		"owner": "current",
	}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	assertStructuredRejectCode(t, err, "GIT_INTERNALS_WRITE_DENIED")
}

func TestNativeCommandChownExactRedirect(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("chown is unavailable on Windows")
	}
	account, err := user.Current()
	testutil.FailErr(t, "resolve current account", err)
	exec := securityNativeExecutor(t)
	root := t.TempDir()
	_, err = exec.Invoke(context.Background(), "command", map[string]any{"command": "chown " + account.Uid + ":" + account.Gid + " scripts/run.sh"}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	assertStructuredRejectCode(t, err, "USE_CHOWN_NATIVE")
}

func TestNativeCommandFindHabitRedirectE2E(t *testing.T) {
	exec := securityNativeExecutor(t)
	_, err := exec.Invoke(context.Background(), "command", map[string]any{
		"command": "find . -name '*.go'",
	}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "explore_readonly"},
	})
	if err == nil || !strings.Contains(err.Error(), "USE_FIND_NOT_COMMAND") {
		t.Fatalf("err = %v want USE_FIND_NOT_COMMAND redirect", err)
	}
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
