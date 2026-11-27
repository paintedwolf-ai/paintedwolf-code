package app

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/app/configuration"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/mcp/mcptest"
	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestPortablePythonLiveEditorSubdirectoryAndExistingFileEdit reproduces two
// real-session regressions uncovered during live-agent qualification:
//  1. HostDocumentStore called EditorBridge.CaptureDraft (which invokes
//     editordoc.Service.OpenText) BEFORE os.Lstat, causing directory stat and
//     nested file opens (src, tests, before/uv.lock) to fail with EISDIR.
//  2. toolhost.editorDocumentsBridge.PublishDraft omitted OperationID on
//     tools.EditorDocumentEdit, causing editordoc.ApplyAgentEdits to reject
//     every edit to an existing text file with ErrOperationConflict.
func TestPortablePythonLiveEditorSubdirectoryAndExistingFileEdit(t *testing.T) {
	ctx := t.Context()
	cfgDir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "app-python-editor-reg.db")
	mcpOverride := filepath.Join(t.TempDir(), "mcp-override.yaml")

	existingEngineRoot := strings.TrimSpace(os.Getenv(configlayout.EnvEngineRoot))
	hasStaged := existingEngineRoot != "" && isRegularFile(filepath.Join(existingEngineRoot, "portable-python", "image", "image-manifest.json"))
	if !hasStaged {
		sourceImg := strings.TrimSpace(os.Getenv(portablepython.EnvImageDir))
		if sourceImg == "" {
			t.Skip("neither staged LYCAON_ENGINE_ROOT nor PW_PORTABLE_PYTHON_IMAGE_DIR is set")
		}
		engineRoot := t.TempDir()
		stagedParent := filepath.Join(engineRoot, "portable-python")
		testutil.FailErr(t, "mkdir staged engine-root", os.MkdirAll(stagedParent, 0o755))
		testutil.FailErr(t, "symlink portable python image", os.Symlink(sourceImg, filepath.Join(stagedParent, "image")))
		t.Setenv(configlayout.EnvEngineRoot, engineRoot)
	}
	t.Setenv(portablepython.EnvImageDir, "")
	t.Setenv(configdir.EnvConfigDir, cfgDir)
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "py-api-reopen-token")
	configtest.Overlay(t, map[config.Rel]string{
		config.DistroMCP: "providers:\n  - id: svca\n    command: \"true\"\n    args: []\n    enabled: false\n",
	})

	wsDir := t.TempDir()
	wsDir, err := filepath.EvalSymlinks(wsDir)
	testutil.FailErr(t, "eval symlinks wsDir", err)
	testutil.FailErr(t, "mkdir src", os.MkdirAll(filepath.Join(wsDir, "src"), 0o755))
	testutil.FailErr(t, "mkdir tests", os.MkdirAll(filepath.Join(wsDir, "tests"), 0o755))
	testutil.FailErr(t, "mkdir before/train-host", os.MkdirAll(filepath.Join(wsDir, "before", "train-host"), 0o755))
	testutil.FailErr(t, "seed src/calc.py", os.WriteFile(filepath.Join(wsDir, "src", "calc.py"), []byte("def add(a, b):\n    return a - b\n"), 0o644))
	testutil.FailErr(t, "seed tests/test_calc.py", os.WriteFile(filepath.Join(wsDir, "tests", "test_calc.py"), []byte("assert True\n"), 0o644))
	testutil.FailErr(t, "seed before/uv.lock", os.WriteFile(filepath.Join(wsDir, "before", "uv.lock"), []byte("version = 1\n"), 0o644))
	testutil.FailErr(t, "seed before/train-host/requirements-cuda.txt", os.WriteFile(filepath.Join(wsDir, "before", "train-host", "requirements-cuda.txt"), []byte("torch==2.5.1\n"), 0o644))

	cfg := configuration.Config{
		DBPath:                    dbPath,
		ListenAddr:                "127.0.0.1:0",
		ConfigRoot:                configlayout.FindModuleRoot(),
		TestMCPConnector:          &mcptest.Connector{Tools: map[string][]*sdkmcp.Tool{"svca": {{Name: "do"}}}},
		TestMCPGlobalOverridePath: mcpOverride,
	}
	appInst, err := Build(ctx, cfg)
	testutil.FailErr(t, "app.Build", err)
	defer func() { _ = appInst.Close() }()

	ts := httptest.NewServer(appInst.Server)
	defer ts.Close()

	proj := mustCreateProjectHTTP(t, ts.URL, wsDir)
	sess := mustCreateSessionHTTP(t, ts.URL, proj.ID)
	rootID := proj.Roots[0].ID

	// Wire the REAL production editorDocumentsAdapter (just as build_server.go does).
	mkTC := func(callID string) tools.ToolContext {
		return tools.ToolContext{
			Identity: tools.InvocationIdentity{
				ProjectID:     proj.ID,
				SessionID:     sess.ID,
				RootSessionID: sess.ID,
				Agent:         "coordinator",
				UserTurn:      1,
				ToolCallID:    callID,
			},
			Source: tools.InvocationSource{
				SourceWorkspaceKind: wire.SourceWorkspaceKindProject,
				ProjectSourceBranch: sourcebranch.Trunk,
				ActiveRootID:        rootID,
				SourceLedger:        appInst.Server.Sources.Workspace.SourceLedger,
				EditorDocuments:     editorDocumentsAdapter{service: appInst.Server.Sources.Workspace.Watch.EditorDocuments},
				Roots: []projectroot.RootRef{{
					ID:    rootID,
					Label: "main",
					Path:  wsDir,
				}},
			},
		}
	}

	runPy := func(callID, code string) portablepython.ToolResponse {
		t.Helper()
		raw, err := appInst.ToolRegistry.Run(ctx, "python", map[string]any{
			"code": code,
		}, mkTC(callID))
		testutil.FailErr(t, "execute python "+callID, err)
		var resp portablepython.ToolResponse
		testutil.FailErr(t, "decode tool response "+callID, json.Unmarshal([]byte(raw), &resp))
		if resp.ExitCode != 0 || !resp.CleanCompletion {
			t.Fatalf("%s failed: exit=%d clean=%v stderr=%q settlement=%+v", callID, resp.ExitCode, resp.CleanCompletion, resp.Stderr, resp.Settlement)
		}
		if resp.Settlement != nil && !resp.Settlement.CleanCompletion {
			t.Fatalf("%s settlement not clean: %+v", callID, resp.Settlement)
		}
		return resp
	}

	// 1. Subdirectory stat, glob, and nested file reads must succeed without EISDIR.
	resp1 := runPy("call-subdirs", `
import glob, os
assert os.path.exists("src") and os.path.isdir("src"), "src must exist as dir"
assert os.path.exists("tests") and os.path.isdir("tests"), "tests must exist as dir"
files = sorted(glob.glob("src/*.py") + glob.glob("tests/*.py"))
assert files == ["src/calc.py", "tests/test_calc.py"], f"unexpected files: {files}"
with open("before/uv.lock") as f:
    lock = f.read().strip()
with open("before/train-host/requirements-cuda.txt") as f:
    req = f.read().strip()
assert lock == "version = 1", lock
assert req == "torch==2.5.1", req
with open("inventory.csv", "w") as f:
    f.write("path,lines\n")
    for p in files:
        with open(p) as sf:
            f.write(f"{p},{sum(1 for _ in sf)}\n")
print("OK_SUBDIRS")
`)
	if !strings.Contains(resp1.Stdout, "OK_SUBDIRS") {
		t.Fatalf("unexpected stdout: %q", resp1.Stdout)
	}
	invBytes, err := os.ReadFile(filepath.Join(wsDir, "inventory.csv"))
	testutil.FailErr(t, "read inventory.csv", err)
	if string(invBytes) != "path,lines\nsrc/calc.py,2\ntests/test_calc.py,1\n" {
		t.Fatalf("unexpected inventory.csv: %q", string(invBytes))
	}

	// 2. Overwriting an EXISTING text file (inventory.csv) and editing an EXISTING
	// subdirectory text file (src/calc.py) via live EditorDocuments must succeed cleanly.
	resp2 := runPy("call-edit-existing", `
with open("inventory.csv", "w") as f:
    f.write("path,lines\nsrc/calc.py,2\ntests/test_calc.py,1\n# updated\n")
with open("src/calc.py", "r") as f:
    src = f.read()
with open("src/calc.py", "w") as f:
    f.write(src.replace("return a - b", "return a + b"))
print("OK_EDIT_EXISTING")
`)
	if !strings.Contains(resp2.Stdout, "OK_EDIT_EXISTING") {
		t.Fatalf("unexpected stdout: %q", resp2.Stdout)
	}
	calcBytes, err := os.ReadFile(filepath.Join(wsDir, "src", "calc.py"))
	testutil.FailErr(t, "read src/calc.py", err)
	if !strings.Contains(string(calcBytes), "return a + b") {
		t.Fatalf("src/calc.py edit did not persist: %q", string(calcBytes))
	}
	invBytes2, err := os.ReadFile(filepath.Join(wsDir, "inventory.csv"))
	testutil.FailErr(t, "read updated inventory.csv", err)
	if !strings.Contains(string(invBytes2), "# updated") {
		t.Fatalf("inventory.csv overwrite did not persist: %q", string(invBytes2))
	}
}
