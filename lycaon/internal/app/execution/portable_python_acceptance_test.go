package execution_test

import (
	"archive/zip"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/app/execution"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/pythonadmission"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	pporacle "github.com/lycaon/lycaon/test/contract/portablepython"
)

func buildInstalledExecutionRuntime(t *testing.T) (execution.Runtime, string) {
	t.Helper()
	cfgDir := t.TempDir()
	existingEngineRoot := strings.TrimSpace(os.Getenv(configlayout.EnvEngineRoot))
	hasStagedDist := existingEngineRoot != "" && fileExists(filepath.Join(existingEngineRoot, "portable-python", "image", "image-manifest.json"))
	if !hasStagedDist {
		sourceImageDir := strings.TrimSpace(os.Getenv(portablepython.EnvImageDir))
		if sourceImageDir == "" {
			t.Skip("neither staged LYCAON_ENGINE_ROOT nor PW_PORTABLE_PYTHON_IMAGE_DIR is set; skipping installed-session Portable Python acceptance suite")
		}
		engineRoot := t.TempDir()
		stagedParent := filepath.Join(engineRoot, "portable-python")
		testutil.FailErr(t, "mkdir staged engine-root parent", os.MkdirAll(stagedParent, 0o755))
		testutil.FailErr(t, "symlink verified image into engine-root", os.Symlink(sourceImageDir, filepath.Join(stagedParent, "image")))
		t.Setenv(configlayout.EnvEngineRoot, engineRoot)
	}

	t.Setenv(configdir.EnvConfigDir, cfgDir)
	t.Setenv(portablepython.EnvImageDir, "")

	catalog := extpackstest.StockCatalog(t)
	rt, err := execution.Build(nil, "", catalog, decide.Reranker{}, nil)
	testutil.FailErr(t, "execution.Build with staged engine-root image", err)
	if rt.Host.Commands.PortablePython() == nil {
		t.Fatal("expected execution.Build to auto-wire portablepython.Service")
	}
	t.Cleanup(func() {
		if svc := rt.Host.Commands.PortablePython(); svc != nil {
			_ = svc.Close(context.Background())
		}
	})
	return rt, cfgDir
}

func runInstalledPython(t *testing.T, ctx context.Context, rt execution.Runtime, args map[string]any, tc tools.ToolContext) portablepython.ToolResponse {
	t.Helper()
	raw, err := rt.Host.Registry.Run(ctx, "python", args, tc)
	testutil.FailErr(t, "run installed python tool", err)
	var resp portablepython.ToolResponse
	testutil.FailErr(t, "decode installed python ToolResponse", json.Unmarshal([]byte(raw), &resp))
	return resp
}

func workspaceTC(sessionID, agent, surface, wsDir string) tools.ToolContext {
	return tools.ToolContext{
		Identity: tools.InvocationIdentity{
			ProjectID:  "proj-acceptance",
			SessionID:  sessionID,
			Agent:      agent,
			ToolCallID: "call-" + sessionID,
		},
		Turn: tools.InvocationTurn{
			TurnSurfaceID: surface,
		},
		Source: tools.InvocationSource{
			SourceWorkspaceKind: api.SourceWorkspaceKindProject,
			ActiveRootID:        "root-main",
			Roots: []projectroot.RootRef{{
				ID:    "root-main",
				Label: "main",
				Path:  wsDir,
			}},
		},
	}
}

func loadAcceptanceFixtureZip(t *testing.T) map[string][]byte {
	t.Helper()
	zipPath := filepath.Join("..", "..", "..", "test", "contract", "portablepython", "testdata", "source-fixtures.zip")
	zr, err := zip.OpenReader(zipPath)
	testutil.FailErr(t, "open source-fixtures.zip", err)
	defer func() { _ = zr.Close() }()
	out := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		testutil.FailErr(t, "open zip entry "+f.Name, err)
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		testutil.FailErr(t, "read zip entry "+f.Name, err)
		out[f.Name] = data
	}
	return out
}

func extractZipWorkflow(t *testing.T, files map[string][]byte, prefix string) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		if !strings.HasPrefix(name, prefix+"/") {
			continue
		}
		rel := strings.TrimPrefix(name, prefix+"/")
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir "+dst, os.MkdirAll(filepath.Dir(dst), 0o755))
		testutil.FailErr(t, "write "+dst, os.WriteFile(dst, data, 0o644))
	}
	return dir
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	testutil.FailErr(t, "copy tree "+src, err)
}

type acceptanceEditorStub struct {
	tools.EditorDocuments
	mu       sync.Mutex
	docs     map[string]tools.EditorDocumentText
	pubCount int
}

func (e *acceptanceEditorStub) OpenDocument(_ context.Context, _ string, _ sourcebranch.ID, rootID, relPath string) (tools.EditorDocumentText, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if d, ok := e.docs[rootID+":"+relPath]; ok {
		return d, true, nil
	}
	if d, ok := e.docs[relPath]; ok {
		return d, true, nil
	}
	return tools.EditorDocumentText{}, false, nil
}

func (e *acceptanceEditorStub) ApplyAgentEdit(_ context.Context, _ string, edit tools.EditorDocumentEdit) (tools.EditorDocumentApplied, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for k, d := range e.docs {
		if d.ID == edit.DocumentID {
			if edit.ExpectedRevision != 0 && edit.ExpectedRevision != d.Revision {
				return tools.EditorDocumentApplied{}, tools.ErrEditorDocumentMoved
			}
			d.Revision++
			d.Text = edit.Content
			d.Dirty = true
			e.docs[k] = d
			e.pubCount++
			return tools.EditorDocumentApplied{Document: d, Saved: false}, nil
		}
	}
	return tools.EditorDocumentApplied{}, tools.ErrEditorDocumentMoved
}

func (e *acceptanceEditorStub) FreezeAgentReads(_, _ string) map[string]documentcore.ReadBasis {
	return nil
}

// TestInstalledSessionAcceptanceSuite exercises the coordinator's installed-session
// acceptance matrix through the real execution.Build composition root with
// PW_PORTABLE_PYTHON_IMAGE_DIR unset.
func TestInstalledSessionAcceptanceSuite(t *testing.T) {
	rt, cfgDir := buildInstalledExecutionRuntime(t)
	ctx := context.Background()

	// 1. All 6 frozen workloads through the real app `python` tool using DefaultResourceLimits.
	t.Run("all 6 frozen workloads", func(t *testing.T) {
		files := loadAcceptanceFixtureZip(t)

		// 1a. dependency-review
		depDir := extractZipWorkflow(t, files, "dependency-review")
		resp := runInstalledPython(t, ctx, rt, map[string]any{"script": "program.py"}, workspaceTC("sess-dep", "implement", "", depDir))
		if !resp.CleanCompletion || resp.ExitCode != 0 {
			t.Fatalf("dependency-review failed: exit=%d clean=%v stderr=%q stop=%+v", resp.ExitCode, resp.CleanCompletion, resp.Stderr, resp.Stop)
		}
		depCSV, err := os.ReadFile(filepath.Join(depDir, "dependency-changes.csv"))
		testutil.FailErr(t, "read dependency-changes.csv", err)
		depExp, err := pporacle.ComputeDepDiff(
			files["dependency-review/before/uv.lock"],
			files["dependency-review/after/uv.lock"],
			files["dependency-review/before/train-host/requirements-cuda.txt"],
			files["dependency-review/after/train-host/requirements-cuda.txt"],
		)
		testutil.FailErr(t, "compute dep diff", err)
		testutil.FailErr(t, "grade dependency-review", pporacle.GradeDependencyDiff(depExp, depCSV, map[string][]string{"before": nil, "after": nil}))

		// 1b. licenses
		licDir := extractZipWorkflow(t, files, "licenses")
		resp = runInstalledPython(t, ctx, rt, map[string]any{"script": "program.py"}, workspaceTC("sess-lic", "implement", "", licDir))
		if !resp.CleanCompletion || resp.ExitCode != 0 {
			t.Fatalf("licenses failed: exit=%d clean=%v stderr=%q stop=%+v", resp.ExitCode, resp.CleanCompletion, resp.Stderr, resp.Stop)
		}
		licCSV, err := os.ReadFile(filepath.Join(licDir, "holdout_repos.csv"))
		testutil.FailErr(t, "read holdout_repos.csv", err)
		testutil.FailErr(t, "grade licenses", pporacle.GradeLicenseCSV(files["licenses/config/repos.yaml"], licCSV))

		// 1c. workbook (openpyxl)
		wbDir := extractZipWorkflow(t, files, "workbook")
		resp = runInstalledPython(t, ctx, rt, map[string]any{"script": "program.py"}, workspaceTC("sess-wb", "implement", "", wbDir))
		if !resp.CleanCompletion || resp.ExitCode != 0 {
			t.Fatalf("workbook failed: exit=%d clean=%v stderr=%q stop=%+v", resp.ExitCode, resp.CleanCompletion, resp.Stderr, resp.Stop)
		}
		wbBytes, err := os.ReadFile(filepath.Join(wbDir, "repositories.xlsx"))
		testutil.FailErr(t, "read repositories.xlsx", err)
		testutil.FailErr(t, "grade workbook", pporacle.GradeRepoWorkbookWithMode(files["workbook/config/repos.yaml"], wbBytes, pporacle.WorkbookFrozenReplay))

		// 1d. release-index
		relDir := extractZipWorkflow(t, files, "release-index")
		resp = runInstalledPython(t, ctx, rt, map[string]any{"script": "program.py"}, workspaceTC("sess-rel", "implement", "", relDir))
		if !resp.CleanCompletion || resp.ExitCode != 0 {
			t.Fatalf("release-index failed: exit=%d clean=%v stderr=%q stop=%+v", resp.ExitCode, resp.CleanCompletion, resp.Stderr, resp.Stop)
		}
		relJSON, err := os.ReadFile(filepath.Join(relDir, "release-index.json"))
		testutil.FailErr(t, "read release-index.json", err)
		relAnchors := make(map[string][]byte)
		for name, data := range files {
			if strings.HasPrefix(name, "release-index/releases/") && strings.HasSuffix(name, ".json") {
				relAnchors[path.Base(name)] = data
			}
		}
		testutil.FailErr(t, "grade release-index", pporacle.GradeReleaseIndex(relAnchors, relJSON))

		// 1e. markdown-links
		mdDir := extractZipWorkflow(t, files, "markdown-links")
		resp = runInstalledPython(t, ctx, rt, map[string]any{"script": "program.py"}, workspaceTC("sess-md", "implement", "", mdDir))
		if !resp.CleanCompletion || resp.ExitCode != 0 {
			t.Fatalf("markdown-links failed: exit=%d clean=%v stderr=%q stop=%+v", resp.ExitCode, resp.CleanCompletion, resp.Stderr, resp.Stop)
		}
		linksCSV, err := os.ReadFile(filepath.Join(mdDir, "links.csv"))
		testutil.FailErr(t, "read links.csv", err)
		mdInputs := make(map[string]string)
		for name, data := range files {
			if strings.HasPrefix(name, "markdown-links/") && strings.HasSuffix(name, ".md") {
				mdInputs[strings.TrimPrefix(name, "markdown-links/")] = string(data)
			}
		}
		testutil.FailErr(t, "grade markdown-links", pporacle.GradeMarkdownLinks(mdInputs, linksCSV))

		// 1f. archive (two distinct reproducible outputs)
		archDir := extractZipWorkflow(t, files, "archive")
		respA := runInstalledPython(t, ctx, rt, map[string]any{"script": "build_handoff.py", "args": []any{"handoff-a.zip"}}, workspaceTC("sess-arch-a", "implement", "", archDir))
		if !respA.CleanCompletion || respA.ExitCode != 0 {
			t.Fatalf("archive run A failed: %+v", respA)
		}
		respB := runInstalledPython(t, ctx, rt, map[string]any{"script": "build_handoff.py", "args": []any{"handoff-b.zip"}}, workspaceTC("sess-arch-b", "implement", "", archDir))
		if !respB.CleanCompletion || respB.ExitCode != 0 {
			t.Fatalf("archive run B failed: %+v", respB)
		}
		zipA, err := os.ReadFile(filepath.Join(archDir, "handoff-a.zip"))
		testutil.FailErr(t, "read handoff-a.zip", err)
		zipB, err := os.ReadFile(filepath.Join(archDir, "handoff-b.zip"))
		testutil.FailErr(t, "read handoff-b.zip", err)
		archAnchors := make(map[string][]byte)
		for name, data := range files {
			if strings.HasPrefix(name, "archive/releases/") && strings.HasSuffix(name, ".json") {
				archAnchors[path.Base(name)] = data
			}
		}
		testutil.FailErr(t, "grade reproducible archive", pporacle.GradeReleaseArchive(archAnchors, zipA, zipB))
	})

	// 2. All 3 extended workloads through the real app `python` tool.
	t.Run("all 3 extended workloads", func(t *testing.T) {
		extBase := filepath.Join("..", "..", "..", "test", "contract", "portablepython", "testdata", "extended-workloads")

		// 2a. calculated-edit + differing editor draft vs disk revision + single publication
		calcDir := t.TempDir()
		copyTree(t, filepath.Join(extBase, "calculated-edit"), calcDir)
		// Seed a differing unsaved editor draft on a sidecar note and verify guest sees draft + publishes once.
		testutil.FailErr(t, "write disk draft.txt", os.WriteFile(filepath.Join(calcDir, "draft.txt"), []byte("DISK_OLD\n"), 0o644))
		editor := &acceptanceEditorStub{
			docs: map[string]tools.EditorDocumentText{
				"root-main:draft.txt": {
					ID:       "doc-calc-draft",
					RootID:   "root-main",
					Path:     "draft.txt",
					Revision: 3,
					Text:     "EDITOR_UNSAVED_REV3\n",
					Dirty:    true,
				},
			},
		}
		calcTC := workspaceTC("sess-calc", "implement", "", calcDir)
		calcTC.Source.EditorDocuments = editor
		draftResp := runInstalledPython(t, ctx, rt, map[string]any{
			"code": `s = open("draft.txt").read().strip(); open("draft.txt", "w").write(s + "+EDITED\n"); print(s)`,
		}, calcTC)
		if !draftResp.CleanCompletion || strings.TrimSpace(draftResp.Stdout) != "EDITOR_UNSAVED_REV3" {
			t.Fatalf("editor draft read/edit failed: %+v", draftResp)
		}
		if editor.pubCount != 1 {
			t.Fatalf("expected single editor publication, got %d", editor.pubCount)
		}
		reopenDoc, ok, err := editor.OpenDocument(ctx, "proj-acceptance", "", "root-main", "draft.txt")
		testutil.FailErr(t, "reopen edited draft", err)
		if !ok || strings.TrimSpace(reopenDoc.Text) != "EDITOR_UNSAVED_REV3+EDITED" {
			t.Fatalf("reopened editor draft mismatch: ok=%v doc=%+v", ok, reopenDoc)
		}

		// Now run edit_program.py and verify_program.py in separate calls.
		r1 := runInstalledPython(t, ctx, rt, map[string]any{"script": "edit_program.py"}, workspaceTC("sess-calc-1", "implement", "", calcDir))
		if !r1.CleanCompletion || r1.ExitCode != 0 {
			t.Fatalf("calculated-edit edit_program.py failed: %+v", r1)
		}
		r2 := runInstalledPython(t, ctx, rt, map[string]any{"script": "verify_program.py"}, workspaceTC("sess-calc-2", "implement", "", calcDir))
		if !r2.CleanCompletion || r2.ExitCode != 0 {
			t.Fatalf("calculated-edit verify_program.py failed: %+v", r2)
		}
		provBytes, err := os.ReadFile(filepath.Join(calcDir, "provenance_result.json"))
		testutil.FailErr(t, "read provenance_result.json", err)
		var provRes struct {
			SHA256         string `json:"sha256"`
			ReadChunkSizes []int  `json:"read_chunk_sizes"`
			MaxChunkSize   int    `json:"max_chunk_size"`
		}
		testutil.FailErr(t, "parse provenance_result.json", json.Unmarshal(provBytes, &provRes))
		alphaBytes, err := os.ReadFile(filepath.Join(calcDir, "inputs", "alpha.bin"))
		testutil.FailErr(t, "read alpha.bin", err)
		betaBytes, err := os.ReadFile(filepath.Join(calcDir, "inputs", "beta.bin"))
		testutil.FailErr(t, "read beta.bin", err)
		testutil.FailErr(t, "grade calculated-edit", pporacle.GradeStreamedProvenance(
			map[string][]byte{"alpha.bin": alphaBytes, "beta.bin": betaBytes},
			pporacle.StreamedProvenanceObs{
				CandidateSHA256: provRes.SHA256,
				ReadChunkSizes:  provRes.ReadChunkSizes,
				MaxChunkSize:    provRes.MaxChunkSize,
			},
		))

		// 2b. project-math-cwd (real guest cwd="bench", asserts portable WASI interpreter, unittest fail -> pass)
		mathDir := t.TempDir()
		copyTree(t, filepath.Join(extBase, "project-math-cwd"), mathDir)
		benchDir := filepath.Join(mathDir, "bench")
		interpResp := runInstalledPython(t, ctx, rt, map[string]any{
			"code": `import os, sys; print(sys.platform + "|" + os.getcwd())`,
			"cwd":  "bench",
		}, workspaceTC("sess-math-interp", "implement", "", mathDir))
		if !interpResp.CleanCompletion || !strings.Contains(interpResp.Stdout, "wasi|/workspace/bench") {
			t.Fatalf("expected wasi interpreter in /workspace/bench, got %q", interpResp.Stdout)
		}
		evalResp := runInstalledPython(t, ctx, rt, map[string]any{
			"script": "eval_program.py",
			"cwd":    "bench",
		}, workspaceTC("sess-math-eval", "implement", "", mathDir))
		if !evalResp.CleanCompletion || evalResp.ExitCode != 0 {
			t.Fatalf("project-math-cwd eval_program.py failed: %+v", evalResp)
		}
		evalBytes, err := os.ReadFile(filepath.Join(benchDir, "eval_result.json"))
		testutil.FailErr(t, "read eval_result.json", err)
		var evalRes struct {
			Ratios               map[string]string `json:"ratios"`
			InvalidInputRejected bool              `json:"invalid_input_rejected"`
		}
		testutil.FailErr(t, "parse eval_result.json", json.Unmarshal(evalBytes, &evalRes))
		ratios := make(map[[3]int64]string, len(evalRes.Ratios))
		for k, v := range evalRes.Ratios {
			parts := strings.Split(k, ",")
			c, _ := strconv.ParseInt(parts[0], 10, 64)
			n, _ := strconv.ParseInt(parts[1], 10, 64)
			kv, _ := strconv.ParseInt(parts[2], 10, 64)
			ratios[[3]int64{c, n, kv}] = v
		}
		testPath := filepath.Join(benchDir, "test_sampling.py")
		origTest, err := os.ReadFile(testPath)
		testutil.FailErr(t, "read test_sampling.py", err)
		brokenTest := strings.Replace(string(origTest), "Fraction(3, 10)", "Fraction(9, 10)", 1)
		testutil.FailErr(t, "inject failing assertion", os.WriteFile(testPath, []byte(brokenTest), 0o644))
		failUT := runInstalledPython(t, ctx, rt, map[string]any{
			"module": "unittest",
			"args":   []any{"discover"},
			"cwd":    "bench",
		}, workspaceTC("sess-math-fail", "implement", "", mathDir))
		testutil.FailErr(t, "repair test_sampling.py", os.WriteFile(testPath, origTest, 0o644))
		passUT := runInstalledPython(t, ctx, rt, map[string]any{
			"module": "unittest",
			"args":   []any{"discover"},
			"cwd":    "bench",
		}, workspaceTC("sess-math-pass", "implement", "", mathDir))
		testutil.FailErr(t, "grade project-math-cwd", pporacle.GradeProjectMathAndTests(pporacle.ProjectMathObs{
			Ratios:               ratios,
			InvalidInputRejected: evalRes.InvalidInputRejected,
			FailRun:              pporacle.UnittestRunObs{ExitCode: failUT.ExitCode, Stderr: failUT.Stderr, Passed: failUT.ExitCode == 0},
			PassRun:              pporacle.UnittestRunObs{ExitCode: passUT.ExitCode, Stderr: passUT.Stderr, Passed: passUT.ExitCode == 0},
		}))

		// 2c. split-preservation (inspects actual prior split files on disk after rejection)
		splitDir := t.TempDir()
		copyTree(t, filepath.Join(extBase, "split-preservation"), splitDir)
		outDir := filepath.Join(splitDir, "out")
		copyTree(t, filepath.Join(splitDir, "seed"), outDir)
		readDirFiles := func(dir string) map[string][]byte {
			entries, err := os.ReadDir(dir)
			testutil.FailErr(t, "readdir "+dir, err)
			m := make(map[string][]byte, len(entries))
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				b, err := os.ReadFile(filepath.Join(dir, e.Name()))
				testutil.FailErr(t, "read "+e.Name(), err)
				m[e.Name()] = b
			}
			return m
		}
		origSplits := readDirFiles(outDir)
		invResp := runInstalledPython(t, ctx, rt, map[string]any{
			"script": "splitter.py",
			"args":   []any{"inputs/late_invalid.jsonl", "out"},
		}, workspaceTC("sess-split-inv", "implement", "", splitDir))
		afterInvalid := readDirFiles(outDir)

		ovlResp := runInstalledPython(t, ctx, rt, map[string]any{
			"script": "splitter.py",
			"args":   []any{"inputs/group_overlap.jsonl", "out"},
		}, workspaceTC("sess-split-ovl", "implement", "", splitDir))
		afterOverlap := readDirFiles(outDir)

		ok1 := runInstalledPython(t, ctx, rt, map[string]any{
			"script": "splitter.py",
			"args":   []any{"inputs/valid.jsonl", "out1"},
		}, workspaceTC("sess-split-ok1", "implement", "", splitDir))
		ok2 := runInstalledPython(t, ctx, rt, map[string]any{
			"script": "splitter.py",
			"args":   []any{"inputs/valid.jsonl", "out2"},
		}, workspaceTC("sess-split-ok2", "implement", "", splitDir))
		if !ok1.CleanCompletion || !ok2.CleanCompletion {
			t.Fatalf("valid split runs failed: ok1=%+v ok2=%+v", ok1, ok2)
		}
		expectedValid := map[string][]byte{
			"train.jsonl": []byte("{\"group\":\"g1\",\"id\":1,\"split\":\"train\"}\n{\"group\":\"g1\",\"id\":2,\"split\":\"train\"}\n"),
			"val.jsonl":   []byte("{\"group\":\"g2\",\"id\":3,\"split\":\"val\"}\n"),
		}
		testutil.FailErr(t, "grade split-preservation", pporacle.GradeSplitPreservation(pporacle.SplitPreservationObs{
			Original:        origSplits,
			AfterInvalid:    afterInvalid,
			InvalidRejected: invResp.ExitCode != 0,
			AfterOverlap:    afterOverlap,
			OverlapRejected: ovlResp.ExitCode != 0,
			ExpectedValid:   expectedValid,
			ValidRun1:       readDirFiles(filepath.Join(splitDir, "out1")),
			ValidRun2:       readDirFiles(filepath.Join(splitDir, "out2")),
		}))
	})

	// 3. Default pytest + bounded time.sleep interrupted by Stop/cancellation.
	t.Run("pytest module and bounded time.sleep stop", func(t *testing.T) {
		wsDir := t.TempDir()
		testutil.FailErr(t, "write test_smoke.py", os.WriteFile(
			filepath.Join(wsDir, "test_smoke.py"),
			[]byte("def test_add():\n    assert 2 + 2 == 4\n"),
			0o644,
		))
		ptResp := runInstalledPython(t, ctx, rt, map[string]any{
			"module": "pytest",
			"args":   []any{"-q", "-p", "no:cacheprovider"},
		}, workspaceTC("sess-pytest", "implement", "", wsDir))
		if !ptResp.CleanCompletion || ptResp.ExitCode != 0 || !strings.Contains(ptResp.Stdout, "1 passed") {
			t.Fatalf("pytest failed: exit=%d clean=%v stdout=%q stderr=%q stop=%+v",
				ptResp.ExitCode, ptResp.CleanCompletion, ptResp.Stdout, ptResp.Stderr, ptResp.Stop)
		}

		// Bounded time.sleep interrupted by context cancel (Stop).
		stopCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		defer cancel()
		sleepResp := runInstalledPython(t, stopCtx, rt, map[string]any{
			"code": `import time; time.sleep(30); print("SHOULD_NOT_REACH")`,
		}, workspaceTC("sess-sleep-stop", "implement", "", wsDir))
		if sleepResp.CleanCompletion || strings.Contains(sleepResp.Stdout, "SHOULD_NOT_REACH") {
			t.Fatalf("expected time.sleep to stop on cancellation, got %+v", sleepResp)
		}
	})

	// 4. Restricted worker on broader surface, failed profile lookup, and protected paths.
	t.Run("restricted worker on broader surface, failed profile lookup, and protected paths", func(t *testing.T) {
		wsDir := t.TempDir()
		testutil.FailErr(t, "mkdir .git/hooks", os.MkdirAll(filepath.Join(wsDir, ".git", "hooks"), 0o755))
		testutil.FailErr(t, "write .git/config", os.WriteFile(filepath.Join(wsDir, ".git", "config"), []byte("[core]\n"), 0o644))

		// 4a. Restricted worker (worker_readonly) on broader surface (implement_investigate) stays restricted.
		roResp := runInstalledPython(t, ctx, rt, map[string]any{
			"code": `open("worker_ro.txt", "w").write("nope")`,
		}, workspaceTC("sess-worker-ro", "worker_readonly", "implement_investigate", wsDir))
		if roResp.CleanCompletion || roResp.Stop == nil || roResp.Stop.Code != pythonruntime.StopEffectNotAllowed {
			t.Fatalf("worker_readonly on implement_investigate = %+v, want COMMAND_EFFECT_NOT_ALLOWED", roResp)
		}
		if _, err := os.Stat(filepath.Join(wsDir, "worker_ro.txt")); !os.IsNotExist(err) {
			t.Fatalf("worker_ro.txt must not exist, err=%v", err)
		}

		// 4b. Failed / unresolved profile lookup on broader surface fails closed (never widens to pyEffectAll).
		unkResp := runInstalledPython(t, ctx, rt, map[string]any{
			"code": `open("unknown_profile.txt", "w").write("nope")`,
		}, workspaceTC("sess-unknown-prof", "nonexistent_profile_xyz", "implement_investigate", wsDir))
		if unkResp.CleanCompletion || unkResp.Stop == nil || unkResp.Stop.Code != pythonruntime.StopEffectNotAllowed {
			t.Fatalf("unresolved profile on implement_investigate = %+v, want COMMAND_EFFECT_NOT_ALLOWED", unkResp)
		}
		if _, err := os.Stat(filepath.Join(wsDir, "unknown_profile.txt")); !os.IsNotExist(err) {
			t.Fatalf("unknown_profile.txt must not exist, err=%v", err)
		}

		// 4c. Protected paths (.git/hooks/pre-commit and .git/config) blocked even for implement profile.
		for _, prot := range []string{".git/hooks/pre-commit", ".git/config"} {
			pResp := runInstalledPython(t, ctx, rt, map[string]any{
				"code": `import sys; open(sys.argv[1], "w").write("pwned")`,
				"args": []any{prot},
			}, workspaceTC("sess-prot-"+filepath.Base(prot), "implement", "", wsDir))
			if pResp.CleanCompletion || pResp.Stop == nil || pResp.Stop.Code != pythonruntime.StopEffectNotAllowed {
				t.Fatalf("protected path %s = %+v, want COMMAND_EFFECT_NOT_ALLOWED", prot, pResp)
			}
		}
	})

	// 5. Per-call /tmp scratch isolation + orphaned scratch recovery on restart.
	t.Run("per-call /tmp isolation and orphaned scratch cleanup on restart", func(t *testing.T) {
		wsDir := t.TempDir()
		r1 := runInstalledPython(t, ctx, rt, map[string]any{
			"code": `open("/tmp/secret_note.txt", "w").write("call1"); print(open("/tmp/secret_note.txt").read())`,
		}, workspaceTC("sess-tmp-1", "implement", "", wsDir))
		if !r1.CleanCompletion || strings.TrimSpace(r1.Stdout) != "call1" {
			t.Fatalf("first /tmp call failed: %+v", r1)
		}
		r2 := runInstalledPython(t, ctx, rt, map[string]any{
			"code": `import os; print("EXISTS" if os.path.exists("/tmp/secret_note.txt") else "ISOLATED")`,
		}, workspaceTC("sess-tmp-2", "implement", "", wsDir))
		if !r2.CleanCompletion || strings.TrimSpace(r2.Stdout) != "ISOLATED" {
			t.Fatalf("second /tmp call saw prior call scratch: %+v", r2)
		}

		// Plant an orphaned pw-py-scratch-* directory simulating a crashed process,
		// then rebuild execution.Build and verify startup recovery removes it.
		scratchRoot := filepath.Join(cfgDir, "portable-python", "scratch")
		orphanDir := filepath.Join(scratchRoot, "pw-py-scratch-stale-crash")
		testutil.FailErr(t, "plant orphan scratch dir", os.MkdirAll(orphanDir, 0o700))
		testutil.FailErr(t, "plant orphan scratch file", os.WriteFile(filepath.Join(orphanDir, "leftover.tmp"), []byte("stale"), 0o600))

		catalog := extpackstest.StockCatalog(t)
		restarted, err := execution.Build(nil, "", catalog, decide.Reranker{}, nil)
		testutil.FailErr(t, "restart execution.Build for orphan scratch sweep", err)
		t.Cleanup(func() {
			if svc := restarted.Host.Commands.PortablePython(); svc != nil {
				_ = svc.Close(context.Background())
			}
		})
		if _, err := os.Stat(orphanDir); !os.IsNotExist(err) {
			t.Fatalf("orphaned scratch dir %s was not swept on restart: err=%v", orphanDir, err)
		}
	})

	// 6. Held approval (approved once delivers 1 body; declined once delivers 0 bodies).
	t.Run("held approval approved once and declined once", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			body, _ := io.ReadAll(r.Body)
			_, _ = w.Write([]byte("ECHO:" + string(body)))
		}))
		defer srv.Close()

		svc := rt.Host.Commands.PortablePython()
		broker := pythonadmission.NewApprovalBroker(pythonadmission.ApprovalBrokerConfig{})
		holdPolicy := func(_ context.Context, op pythonruntime.ReachedOperation) pythonadmission.PolicyDecision {
			if op.Family == pythonruntime.EffectHTTP {
				return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionHold, Reason: "review outbound POST"}
			}
			return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionAllow}
		}

		// 6a. Approve once -> 1 body delivered.
		go func() {
			for range 200 {
				for _, l := range broker.Registry().List() {
					if l.State == pythonadmission.LeasePending {
						_ = broker.Approve(l.LeaseID, "approved once")
						return
					}
				}
				time.Sleep(15 * time.Millisecond)
			}
		}()
		respOk, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			ExecutionID: "exec-hold-approve",
			CallID:      "call-hold-approve",
			SessionID:   "sess-hold",
			Actor:       "implement",
			View:        "workspace",
			Mode:        pythonruntime.ModeCode,
			Code:        `import requests, sys; r = requests.post(sys.argv[1], data="payload-1"); print(r.text)`,
			Args:        []string{srv.URL + "/hold-ok"},
			HTTP:        portablepython.HTTPRequestOptions{Client: srv.Client()},
			Broker:      broker,
			Policy:      holdPolicy,
		})
		testutil.FailErr(t, "held approval approved execution", err)
		if !respOk.CleanCompletion || strings.TrimSpace(respOk.Stdout) != "ECHO:payload-1" || hits.Load() != 1 {
			t.Fatalf("approved hold result mismatch: resp=%+v hits=%d", respOk, hits.Load())
		}

		// 6b. Decline once -> 0 additional bodies delivered + COMMAND_EFFECT_NOT_ALLOWED.
		go func() {
			for range 200 {
				for _, l := range broker.Registry().List() {
					if l.State == pythonadmission.LeasePending {
						_ = broker.Decline(l.LeaseID, "declined by reviewer")
						return
					}
				}
				time.Sleep(15 * time.Millisecond)
			}
		}()
		respDeny, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			ExecutionID: "exec-hold-decline",
			CallID:      "call-hold-decline",
			SessionID:   "sess-hold",
			Actor:       "implement",
			View:        "workspace",
			Mode:        pythonruntime.ModeCode,
			Code:        `import requests, sys; requests.post(sys.argv[1], data="payload-2")`,
			Args:        []string{srv.URL + "/hold-deny"},
			HTTP:        portablepython.HTTPRequestOptions{Client: srv.Client()},
			Broker:      broker,
			Policy:      holdPolicy,
		})
		testutil.FailErr(t, "held approval declined execution", err)
		if respDeny.CleanCompletion || respDeny.Stop == nil || respDeny.Stop.Code != pythonruntime.StopEffectNotAllowed {
			t.Fatalf("declined hold = %+v, want COMMAND_EFFECT_NOT_ALLOWED", respDeny)
		}
		if hits.Load() != 1 {
			t.Fatalf("declined hold delivered request to server: hits=%d, want 1", hits.Load())
		}
	})

	// 7. Supervised preview lifecycle (start via registered python tool, fetch, track, stop via job controls).
	t.Run("supervised preview lifecycle via session job controls", func(t *testing.T) {
		wsDir := t.TempDir()
		testutil.FailErr(t, "write preview index.html", os.WriteFile(filepath.Join(wsDir, "index.html"), []byte("<h1>installed-preview-ok</h1>"), 0o644))

		prevResp := runInstalledPython(t, ctx, rt, map[string]any{
			"module": "http.server",
			"args":   []any{"--bind", "127.0.0.1", "0"},
		}, workspaceTC("sess-preview", "implement", "", wsDir))
		if !prevResp.CleanCompletion || prevResp.PreviewRecord == nil || !prevResp.PreviewRecord.Active {
			t.Fatalf("expected active preview listener from installed python tool, got %+v", prevResp)
		}
		jobID := prevResp.PreviewRecord.JobID
		addr := prevResp.PreviewRecord.Address

		// Verify tracked by session job controls.
		active := rt.Host.Commands.ActivePythonPreviewJobs()
		found := false
		for _, rec := range active {
			if rec.JobID == jobID && rec.Active {
				found = true
			}
		}
		if !found {
			t.Fatalf("preview job %q not tracked in ActivePythonPreviewJobs: %+v", jobID, active)
		}

		// Verify reachable via loopback HTTP GET.
		httpResp, err := http.Get("http://" + addr + "/index.html")
		testutil.FailErr(t, "GET supervised preview index.html", err)
		body, err := io.ReadAll(httpResp.Body)
		_ = httpResp.Body.Close()
		testutil.FailErr(t, "read preview body", err)
		if httpResp.StatusCode != http.StatusOK || string(body) != "<h1>installed-preview-ok</h1>" {
			t.Fatalf("preview GET = (%d, %q)", httpResp.StatusCode, string(body))
		}

		// Stop through session job controls and verify cleanup.
		testutil.FailErr(t, "stop supervised preview job", rt.Host.Commands.StopPythonPreviewJob(ctx, jobID))
		for _, rec := range rt.Host.Commands.ActivePythonPreviewJobs() {
			if rec.JobID == jobID {
				t.Fatalf("preview job %q still active after StopPythonPreviewJob", jobID)
			}
		}
		if _, getErr := http.Get("http://" + addr + "/index.html"); getErr == nil {
			t.Fatalf("expected preview listener on %s to be closed after StopPythonPreviewJob", addr)
		}
	})
}
