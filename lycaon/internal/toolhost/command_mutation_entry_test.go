//go:build darwin || linux

package toolhost

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

func composedMutationEntry(t *testing.T) (*programtest.Fixture, *projectsource.SourceMutationService, *project.Project) {
	t.Helper()
	fixture := programtest.New(t, programtest.Options{})
	root, err := filepath.EvalSymlinks(fixture.Dir)
	testutil.FailErr(t, "canonicalize root", err)
	fixture.Dir = root
	runtime, err := NewRuntime(RuntimeConfig{ConfigRoot: configlayout.FindModuleRoot(), Catalog: extpackstest.StockCatalog(t)})
	testutil.FailErr(t, "compose production tool host", err)
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load stock rejection guidance", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	runtime.Authority.ApplyGuidanceRejects(guidance.NewStaticRejectFormatter(hints))
	original := fixture.Runtime
	testutil.FailErr(t, "compose production program services", runtime.Commands.SetProgramServices(ProgramServices{Recorder: fixture.Recorder, Blobs: original.Blobs, Projector: original.Projector, Progress: original.Progress, Sessions: original.Sessions}))
	runtime.Commands.SetBackgroundRegistry(fixture.Background)
	fixture.Runtime = runtime.Commands.programs()
	_, err = fixture.Database.ExecContext(t.Context(), `INSERT INTO project_roots (id,project_id,path,label,is_primary,added_at) VALUES ('r1',?,?,'root',1,?)`, testdbseed.DefaultProjectID, root, db.FormatTime(time.Now().UTC()))
	testutil.FailErr(t, "attach durable root", err)
	ledger := sourceledger.New(fixture.Database, t.TempDir())
	service := projectsource.NewSourceMutationService(fixture.Database, ledger)
	t.Cleanup(func() { testutil.FailErr(t, "close source mutation service", service.Close()) })
	scratch, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "canonicalize scratch", err)
	fixture.Configure = func(tc *tools.ToolContext) {
		tc.Identity.Agent = "implement"
		tc.Identity.UserTurn = 1
		tc.Source.SourceLedger, tc.Source.SourceMutations = ledger, service
		tc.Source.History = tools.SourceHistory{Files: ledger.History, Comparison: ledger.Comparisons, Git: ledger.Git, Authorship: ledger.Walk}
		tc.Source.Commands, tc.Source.GitMutations, tc.Source.Observations = ledger.Commands, ledger.Git, ledger.Inventory
		tc.Host.SessionScratchDir = scratch
		tc.Files.FileChangeReview = func(context.Context, []tools.FileChange) error { return nil }
	}
	p := &project.Project{ID: testdbseed.DefaultProjectID, Roots: []project.Root{{ID: "r1", ProjectID: testdbseed.DefaultProjectID, Path: root, IsPrimary: true}}}
	return fixture, service, p
}

func runMutationEntryTree(t *testing.T, total int) (copyElapsed, removeElapsed time.Duration) {
	t.Helper()
	fixture, service, p := composedMutationEntry(t)
	const buckets = 100
	for i := range total {
		dir := filepath.Join(fixture.Dir, "source", fmt.Sprintf("b%02d", i%buckets))
		testutil.FailErr(t, "create source bucket", os.MkdirAll(dir, 0o755))
		testutil.FailErr(t, "seed source file", os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%05d", i)), []byte("ok\n"), 0o644))
	}
	call := func(source string) time.Duration {
		started := time.Now()
		raw, err := fixture.Call(t, t.Context(), source)
		elapsed := time.Since(started)
		testutil.FailErr(t, "run production command entry", err)
		var outcome command.ProgramOutcome
		testutil.FailErr(t, "decode program outcome", json.Unmarshal([]byte(raw), &outcome))
		if outcome.State != "completed" || outcome.Status != 0 {
			t.Fatalf("program %q result: %s", source, raw)
		}
		return elapsed
	}
	copyElapsed = call("cp -r source copied")
	t.Logf("production entry cp %d files elapsed=%s", total, copyElapsed)
	var attributed int
	testutil.FailErr(t, "count attributed copied files", fixture.Database.QueryRowContext(t.Context(), `SELECT count(*) FROM source_effects WHERE path LIKE 'copied/%' AND entry_kind='file'`).Scan(&attributed))
	if attributed != total {
		t.Fatalf("attributed copied files=%d want=%d", attributed, total)
	}
	removeElapsed = call("rm -rf copied")
	t.Logf("production entry rm %d files elapsed=%s", total, removeElapsed)
	if _, err := os.Lstat(filepath.Join(fixture.Dir, "copied")); !os.IsNotExist(err) {
		t.Fatalf("removed tree still exists: %v", err)
	}
	state, err := service.History.State(t.Context(), p.ID)
	testutil.FailErr(t, "read actual undo history", err)
	if state.Undo == nil || state.Undo.Path != "copied" {
		t.Fatalf("single tree undo missing: %+v", state)
	}
	_, err = service.Undo(t.Context(), uuid.NewString(), p, projectsource.SourceHistoryMutationRequest{ExpectedEntryID: state.Undo.ID})
	testutil.FailErr(t, "undo actual tree removal", err)
	restored := 0
	testutil.FailErr(t, "verify all restored files", filepath.WalkDir(filepath.Join(fixture.Dir, "copied"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		bytes, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if string(bytes) != "ok\n" {
			return fmt.Errorf("restored file bytes differ")
		}
		restored++
		return nil
	}))
	if restored != total {
		t.Fatalf("restored files=%d want=%d", restored, total)
	}
	return copyElapsed, removeElapsed
}

func TestComposedMutationEntryCopyRemoveUndo(t *testing.T) {
	runMutationEntryTree(t, 10)
}

func TestComposedRecursiveRemovalPreservesPolicyWithoutReviewer(t *testing.T) {
	fixture, _, _ := composedMutationEntry(t)
	setup := fixture.Configure
	fixture.Configure = func(tc *tools.ToolContext) { setup(tc); tc.Files.FileChangeReview = nil }
	testutil.FailErr(t, "create instruction directory", os.Mkdir(filepath.Join(fixture.Dir, "tree"), 0o755))
	path := filepath.Join(fixture.Dir, "tree", "AGENTS.md")
	testutil.FailErr(t, "seed instructions", os.WriteFile(path, []byte("instructions"), 0o644))
	raw, err := fixture.Call(t, t.Context(), "rm -rf tree")
	testutil.FailErr(t, "run actual command entry", err)
	var outcome command.ProgramOutcome
	testutil.FailErr(t, "decode command outcome", json.Unmarshal([]byte(raw), &outcome))
	if outcome.StopCode != "FILE_CHANGE_APPROVAL_UNAVAILABLE" || outcome.CompletedEffects != 0 {
		t.Fatalf("policy refusal lost: %+v", outcome)
	}
	if outcome.Refusal == nil || outcome.Refusal.Code != outcome.StopCode || outcome.Refusal.Feedback == "" {
		t.Fatalf("rendered policy refusal missing: %+v", outcome.Refusal)
	}
	t.Logf("rendered policy refusal: %s", outcome.Refusal.Feedback)
	content, err := os.ReadFile(path)
	testutil.FailErr(t, "read protected instructions", err)
	if string(content) != "instructions" {
		t.Fatalf("instructions changed: %q", content)
	}
}
