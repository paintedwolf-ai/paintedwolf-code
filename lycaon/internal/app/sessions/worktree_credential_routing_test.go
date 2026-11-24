package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/repochange/repochangetest"
	"github.com/lycaon/lycaon/internal/secretharvest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/search"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func runSessionGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, string(out))
	}
}

func TestSiblingCommandAndExternalProcessEditsPreservePrimaryCredentialsAndRefreshSearch(t *testing.T) {
	repochangetest.Acquire(t)
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())

	f := programtest.New(t, programtest.Options{})
	primary := gitrepo.CanonicalDir(f.Dir)
	f.Dir = primary
	testutil.FailErr(t, "mkdir src", os.MkdirAll(filepath.Join(primary, "src"), 0o755))
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(primary, "src/item.go"), []byte("package src\n// InitialToken\n"), 0o644))
	testutil.FailErr(t, "seed env", os.WriteFile(filepath.Join(primary, ".env"), []byte("API_SECRET=primary-initial-4411\n"), 0o600))
	runSessionGit(t, primary, "init", "-q")
	runSessionGit(t, primary, "add", "src/item.go")
	runSessionGit(t, primary, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "seed")

	home := os.Getenv("PW_TEST_HOST_HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		testutil.FailErr(t, "home dir", err)
	}
	//nolint:usetesting // Linked checkout must reside outside auto-admitted temp locations.
	extParent, err := os.MkdirTemp(home, "paintedwolf-tp04-*")
	testutil.FailErr(t, "mkdir extParent", err)
	t.Cleanup(func() { _ = os.RemoveAll(extParent) })
	extParent = gitrepo.CanonicalDir(extParent)

	siblingWT := filepath.Join(extParent, "sibling-wt")
	unrelatedDir := filepath.Join(extParent, "unrelated")
	testutil.FailErr(t, "mkdir unrelated", os.MkdirAll(unrelatedDir, 0o755))
	runSessionGit(t, primary, "worktree", "add", "-q", "-b", "feat/tp04", siblingWT)

	ledger := f.AttachLedger(t, sourceledger.Timing{After: func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time)
		close(ch)
		return ch
	}})
	var outsideRootID string
	err = f.Database.QueryRowContext(t.Context(),
		`SELECT id FROM project_roots WHERE project_id = ? AND kind = 'outside'`, testdbseed.DefaultProjectID,
	).Scan(&outsideRootID)
	testutil.FailErr(t, "lookup outside root row", err)

	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{0x3c}, 32))
	testutil.FailErr(t, "fingerprinter", err)
	files := &credentialFiles{
		harvest:  secretharvest.NewRuntime(fp),
		fp:       fp,
		queries:  db.New(f.Database),
		managed:  func(string) []secretmatch.Remembered { return nil },
		exposure: func(context.Context, string, string) error { return nil },
	}
	files.index = newCredentialIndex(files)
	files.startCredentialIndex()
	t.Cleanup(func() {
		testutil.FailErr(t, "close credential index", files.closeCredentialIndex(context.Background()))
	})
	testutil.FailErr(t, "admit primary credentials", files.EnsureProject(t.Context(), testdbseed.DefaultProjectID))

	// Escaping paths remain rejected by credential scanning.
	rootEntry := files.index.roots[testdbseed.DefaultProjectID+"\x00r1"]
	if err := files.index.scan(t.Context(), rootEntry, "../sibling-wt/.env", files.harvest.CaptureFiles(testdbseed.DefaultProjectID)); err == nil || !strings.Contains(err.Error(), "leaves root") {
		t.Fatalf("expected escaping path rejection, got %v", err)
	}

	prevConfigure := f.Configure
	f.Configure = func(tc *tools.ToolContext) {
		if prevConfigure != nil {
			prevConfigure(tc)
		}
		tc.Effects.CredentialFiles = files
		tc.Source.OutsideRoot = projectroot.OutsideRef(outsideRootID)
		tc.Files.GrantedWriteRoots = []string{siblingWT, unrelatedDir}
	}

	f.Runtime.Executor.Boundary.SetSessionWriteRootOverlay(func(context.Context, string, string) []string {
		return []string{siblingWT, unrelatedDir}
	})
	releaseGrant := projectpaths.SetGrantedAccessSource(func(_, _, abs string, _ bool) (projectpaths.Access, bool) {
		for _, allowed := range []string{siblingWT, unrelatedDir} {
			if rel, relErr := filepath.Rel(allowed, abs); relErr == nil && (rel == "." || filepath.IsLocal(rel)) {
				return projectpaths.Access{Path: allowed, Tree: true}, true
			}
		}
		return projectpaths.Access{}, false
	})
	t.Cleanup(releaseGrant)

	procCatalog := sourcecatalog.Process()
	searches, err := search.NewOwners(native.ExecutionRevisions{Boundary: f.Runtime.Boundary, Literals: procCatalog.Literals}, f.Recorder, nil)
	testutil.FailErr(t, "new search owners", err)
	searches.Catalog = procCatalog
	f.Runtime.Owners = func(_ tools.ToolContext, _ *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, admission tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		programSearch := *searches
		programSearch.Revisions.Memory = memory
		programSearch.Admission = admission
		s := &programSearch
		natives := map[string]command.NativeOperation{}
		for _, name := range s.Commands() {
			tool, _ := s.Operation(name)
			natives[name] = command.NativeOperation{Tool: tool, Prepare: func(_ context.Context, paths *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				prepared, prepErr := s.Prepare(paths, dispatch, request)
				if prepErr != nil {
					return command.NativePrepared{}, prepErr
				}
				return command.NativePrepared{Mode: search.EffectMode(prepared.Argv), Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
			}}
		}
		return command.ProgramOwners{Natives: natives}, nil
	}

	callStdout := func(cmdLine string) string {
		t.Helper()
		raw, callErr := f.Call(t, t.Context(), cmdLine)
		testutil.FailErr(t, cmdLine, callErr)
		var out struct {
			Stdout string `json:"stdout"`
			Stderr string `json:"stderr"`
			Status int    `json:"status"`
		}
		testutil.FailErr(t, "decode stdout", json.Unmarshal([]byte(raw), &out))
		if out.Status != 0 {
			t.Fatalf("%s exited %d: stderr=%q raw=%s", cmdLine, out.Status, out.Stderr, raw)
		}
		return strings.TrimSpace(out.Stdout)
	}

	// Warm both directory and literal observations on the sibling checkout.
	if got := callStdout(fmt.Sprintf("cd %q && find src -name '*.go' && grep -rl UpdatedCommandToken src || true", siblingWT)); got != "src/item.go" {
		t.Fatalf("unexpected cold search output: %q", got)
	}
	repochange.UseDebounce(t.Context(), repochange.DefaultDebounce)()
	testutil.FailErr(t, "settle initial primary credentials", files.EnsureProject(t.Context(), testdbseed.DefaultProjectID))

	var scanMu sync.Mutex
	var scannedPaths []string
	files.index.onScan = func(rootPath, path string) {
		scanMu.Lock()
		scannedPaths = append(scannedPaths, gitrepo.CanonicalDir(rootPath)+":"+path)
		scanMu.Unlock()
	}

	var eventsMu sync.Mutex
	var primaryEvents []repochange.Event
	t.Cleanup(repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.Kind == repochange.WorktreeChanged && gitrepo.CanonicalDir(ev.ProjectDir) == primary {
			eventsMu.Lock()
			primaryEvents = append(primaryEvents, ev)
			eventsMu.Unlock()
		}
	}))
	primaryEpochBefore := repochange.CurrentEpoch(primary)

	// Part A: Normal native mutation tool edit in sibling worktree + unrelated external write.
	writeTool := &native.WriteTool{Boundary: f.Runtime.Boundary}
	_, err = f.Invoke(t, t.Context(), "write", map[string]any{
		"path":    filepath.Join(siblingWT, "src/item.go"),
		"content": "package src\n// UpdatedCommandToken\n",
	}, writeTool.Run)
	testutil.FailErr(t, "write sibling file", err)
	_, err = f.Invoke(t, t.Context(), "write", map[string]any{
		"path":    filepath.Join(unrelatedDir, "note.txt"),
		"content": "external note\n",
	}, writeTool.Run)
	testutil.FailErr(t, "write unrelated external file", err)
	repochange.UseDebounce(t.Context(), repochange.DefaultDebounce)()

	testutil.FailErr(t, "recheck primary credentials after sibling edit", files.EnsureProject(t.Context(), testdbseed.DefaultProjectID))
	scanMu.Lock()
	eventsMu.Lock()
	if len(scannedPaths) != 0 || len(primaryEvents) != 0 || repochange.CurrentEpoch(primary) != primaryEpochBefore || len(files.index.roots) != 1 {
		t.Fatalf("sibling/external write disturbed primary: scans=%v events=%v roots=%d", scannedPaths, primaryEvents, len(files.index.roots))
	}
	eventsMu.Unlock()
	scanMu.Unlock()

	if got := callStdout(fmt.Sprintf("cd %q && grep -rl UpdatedCommandToken src", siblingWT)); got != "src/item.go" {
		t.Fatalf("next search missed native sibling edit: %q", got)
	}

	// Part B: External-process mutation inside sibling worktree via command window.
	extTarget := filepath.Join(siblingWT, "src/ext_added.go")
	extCmd := fmt.Sprintf("external -- /bin/sh -c 'printf \"package src\\n// ExternalProcToken\\n\" > \"$1\"' sh %q", extTarget)
	_ = callStdout(extCmd)
	testutil.WaitFor(t, "command window settles", func() bool {
		return len(ledger.Commands.OpenSessionWindows(testdbseed.DefaultProjectID, f.Session)) == 0
	})
	repochange.UseDebounce(t.Context(), repochange.DefaultDebounce)()

	testutil.FailErr(t, "recheck primary credentials after external process", files.EnsureProject(t.Context(), testdbseed.DefaultProjectID))
	scanMu.Lock()
	eventsMu.Lock()
	if len(scannedPaths) != 0 || len(primaryEvents) != 0 {
		t.Fatalf("external process sibling edit disturbed primary: scans=%v events=%v", scannedPaths, primaryEvents)
	}
	eventsMu.Unlock()
	scanMu.Unlock()

	if got := callStdout(fmt.Sprintf("cd %q && find src -name '*.go' && grep -rl ExternalProcToken src", siblingWT)); !strings.Contains(got, "src/ext_added.go") {
		t.Fatalf("next search missed external-process sibling file: %q", got)
	}

	// Verify outside effect was recorded in sourceledger.
	var outsideHeads int
	testutil.FailErr(t, "count outside heads", f.Database.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM source_branch_heads WHERE project_id = ? AND root_id = ?`, testdbseed.DefaultProjectID, outsideRootID).Scan(&outsideHeads))
	if outsideHeads == 0 {
		t.Fatal("expected outside file effect to be recorded in sourceledger")
	}

	// Part C: In-root credential edit refreshes the exact relative path without walking the root.
	_, err = f.Invoke(t, t.Context(), "write", map[string]any{
		"path":    ".env.local",
		"content": "LOCAL_SECRET=in-root-credential-8899\n",
	}, writeTool.Run)
	testutil.FailErr(t, "write in-root credential", err)
	repochange.UseDebounce(t.Context(), repochange.DefaultDebounce)()

	scanMu.Lock()
	wantScan := primary + ":.env.local"
	if !slices.Equal(scannedPaths, []string{wantScan}) {
		t.Fatalf("in-root credential scans = %v, want [%s]", scannedPaths, wantScan)
	}
	scanMu.Unlock()

	foundSecret := false
	for _, v := range files.harvest.FileValues(testdbseed.DefaultProjectID) {
		if v.Secret() == "in-root-credential-8899" && !v.Retired {
			foundSecret = true
		}
	}
	if !foundSecret {
		t.Fatal("in-root credential edit did not refresh secret evidence")
	}
}
