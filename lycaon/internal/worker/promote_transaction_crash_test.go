package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

var errPromoteCrash = errors.New("simulated crash")

// crashDuring stops fn where its seam panicked, so nothing after that point runs, as in a process death.
func crashDuring(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != errPromoteCrash {
			t.Fatalf("expected a simulated crash, got %v", r)
		}
	}()
	fn()
}

// promoteCrashFixture exercises every plan shape: edit with an out-of-root alias
// and a held descriptor, delete, and create.
type promoteCrashFixture struct {
	dataDir string
	task    *api.WorkerTask
	promote PromoteRoots
	plans   []promoteMutation
	edited  string
	removed string
	created string
	alias   string
	originA os.FileInfo
	originB os.FileInfo
}

func newPromoteCrashFixture(t *testing.T) *promoteCrashFixture {
	t.Helper()
	return newPromoteCrashFixtureIn(t, t.TempDir())
}

func newPromoteCrashFixtureIn(t *testing.T, dataDir string) *promoteCrashFixture {
	t.Helper()
	root := t.TempDir()
	task := &api.WorkerTask{ID: "job-crash", WorkspacePath: root, WorkspaceRoot: filepath.Join(root, "branch")}
	testutil.FailErr(t, "mkdir branch", os.MkdirAll(task.WorkspaceRoot, 0o755))
	f := &promoteCrashFixture{
		dataDir: dataDir,
		task:    task,
		promote: PromoteRootsForTask(task, nil),
		edited:  filepath.Join(root, "edited.sh"),
		removed: filepath.Join(root, "removed.txt"),
		created: filepath.Join(root, "created.txt"),
		alias:   filepath.Join(t.TempDir(), "alias.sh"),
		plans: []promoteMutation{
			{path: "edited.sh", original: []byte("old-edit\n"), originalExists: true, originalMode: 0o750, target: []byte("new-edit\n"), targetExists: true},
			{path: "removed.txt", original: []byte("old-removed\n"), originalExists: true, originalMode: 0o640},
			{path: "created.txt", target: []byte("new-created\n"), targetExists: true},
		},
	}
	testutil.FailErr(t, "seed edited", os.WriteFile(f.edited, []byte("old-edit\n"), 0o750))
	testutil.FailErr(t, "chmod edited", os.Chmod(f.edited, 0o750))
	testutil.FailErr(t, "seed removed", os.WriteFile(f.removed, []byte("old-removed\n"), 0o640))
	testutil.FailErr(t, "chmod removed", os.Chmod(f.removed, 0o640))
	testutil.FailErr(t, "link alias", os.Link(f.edited, f.alias))
	held, err := os.Open(f.edited)
	testutil.FailErr(t, "hold descriptor", err)
	t.Cleanup(func() { _ = held.Close() })
	f.originA, err = held.Stat()
	testutil.FailErr(t, "stat held descriptor", err)
	f.originB, err = os.Stat(f.removed)
	testutil.FailErr(t, "stat removed", err)
	retained := filepath.Join(f.txDir(), "retained")
	testutil.FailErr(t, "mkdir stale slots", os.MkdirAll(retained, 0o700))
	testutil.FailErr(t, "seed stale slot", os.WriteFile(filepath.Join(retained, "slot-stale"), []byte("stale"), 0o600))
	return f
}

func (f *promoteCrashFixture) txDir() string { return promoteTransactionDir(f.dataDir, f.task.ID) }

func (f *promoteCrashFixture) journalPath() string {
	return filepath.Join(f.txDir(), "transaction.json")
}

// slot names a target's retained original. Targets are recorded in path order.
func (f *promoteCrashFixture) slot(path string) string {
	index := map[string]int{"created.txt": 0, "edited.sh": 1, "removed.txt": 2}[path]
	return filepath.Join(f.txDir(), "retained", fmt.Sprintf("slot-%06d", index))
}

// run stops at the crashAt'th step without running anything after it; a negative crashAt runs to
// completion. observe sees the durable state at each step.
func (f *promoteCrashFixture) run(t *testing.T, crashAt int, observe func(checkpoint.TransactionStep, string)) error {
	t.Helper()
	n := 0
	effects := promoteTransactionEffects{Reached: func(step checkpoint.TransactionStep, path string) error {
		if observe != nil {
			observe(step, path)
		}
		if n == crashAt {
			panic(errPromoteCrash)
		}
		n++
		return nil
	}}
	var applyErr error
	apply := func() {
		var tx *promoteTransaction
		tx, applyErr = beginPromoteTransactionWith(f.dataDir, f.task.ID, f.plans, effects)
		if applyErr == nil {
			applyErr = tx.apply(t.Context(), f.promote, f.task)
		}
	}
	if crashAt < 0 {
		apply()
		return applyErr
	}
	crashDuring(t, apply)
	return nil
}

func (f *promoteCrashFixture) assertOriginals(t *testing.T) {
	t.Helper()
	for path, want := range map[string]string{f.edited: "old-edit\n", f.removed: "old-removed\n"} {
		raw, err := os.ReadFile(path)
		testutil.FailErr(t, "read original", err)
		if string(raw) != want {
			t.Fatalf("%s = %q, want %q", filepath.Base(path), raw, want)
		}
	}
	edited, err := os.Stat(f.edited)
	testutil.FailErr(t, "stat edited", err)
	alias, err := os.Stat(f.alias)
	testutil.FailErr(t, "stat alias", err)
	removed, err := os.Stat(f.removed)
	testutil.FailErr(t, "stat removed", err)
	if !os.SameFile(edited, f.originA) || !os.SameFile(alias, f.originA) || !os.SameFile(removed, f.originB) {
		t.Fatal("recovery did not preserve original inode, alias and held descriptor identity")
	}
	if edited.Mode().Perm() != 0o750 || removed.Mode().Perm() != 0o640 {
		t.Fatalf("modes = %o %o", edited.Mode().Perm(), removed.Mode().Perm())
	}
	if _, err := os.Lstat(f.created); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created path survived recovery: %v", err)
	}
}

func (f *promoteCrashFixture) primarySnapshot(t *testing.T) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	for _, path := range []string{f.edited, f.removed, f.created} {
		raw, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			snapshot[path] = "<absent>"
			continue
		}
		testutil.FailErr(t, "snapshot primary", err)
		snapshot[path] = string(raw)
	}
	return snapshot
}

func (f *promoteCrashFixture) assertSettlesAndRetries(t *testing.T) {
	t.Helper()
	found, err := recoverPromoteTransaction(t.Context(), f.dataDir, f.task.ID, f.promote, f.task)
	testutil.FailErr(t, "recover", err)
	if !found {
		t.Fatal("journal not found")
	}
	f.assertOriginals(t)
	if found, err := recoverPromoteTransaction(t.Context(), f.dataDir, f.task.ID, f.promote, f.task); err != nil || found {
		t.Fatalf("second recovery found=%v err=%v", found, err)
	}
	f.assertOriginals(t)
	if _, err := os.Lstat(f.txDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transaction record survived settlement: %v", err)
	}
	if err := f.run(t, -1, nil); err != nil {
		t.Fatalf("retry after settlement: %v", err)
	}
	if raw, err := os.ReadFile(f.created); err != nil || string(raw) != "new-created\n" {
		t.Fatalf("retry did not land: %q %v", raw, err)
	}
}

func (f *promoteCrashFixture) readJournalDocument(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(f.journalPath())
	testutil.FailErr(t, "read journal", err)
	var document map[string]any
	testutil.FailErr(t, "decode journal", json.Unmarshal(raw, &document))
	return document
}

func (f *promoteCrashFixture) writeJournalDocument(t *testing.T, document map[string]any) {
	t.Helper()
	raw, err := json.Marshal(document)
	testutil.FailErr(t, "encode journal", err)
	testutil.FailErr(t, "write journal", os.WriteFile(f.journalPath(), raw, 0o600))
}

func TestPromoteTransactionCrashPointContract(t *testing.T) {
	type point struct {
		step checkpoint.TransactionStep
		path string
	}
	var points []point
	discovery := newPromoteCrashFixture(t)
	if err := discovery.run(t, -1, func(step checkpoint.TransactionStep, path string) {
		_, rel, _ := strings.Cut(path, ":")
		points = append(points, point{step, rel})
		phase := discovery.readJournalDocument(t)["phase"]
		// The write-ahead invariant: a primary changes only under a durable applying record.
		if started := step == checkpoint.StepApplying || step == checkpoint.StepTargetMutated; started != (phase == "applying") {
			t.Fatalf("%s %s: phase %v", step, path, phase)
		}
		if step != checkpoint.StepTargetMutated {
			discovery.assertOriginals(t)
		}
	}); err != nil {
		t.Fatalf("clean run: %v", err)
	}
	wantPoints := []point{
		{checkpoint.StepPrepared, ""},
		{checkpoint.StepOriginalRetained, "edited.sh"}, {checkpoint.StepOriginalRetained, "removed.txt"},
		{checkpoint.StepApplying, ""},
		{checkpoint.StepTargetMutated, "edited.sh"}, {checkpoint.StepTargetMutated, "removed.txt"}, {checkpoint.StepTargetMutated, "created.txt"},
	}
	if !slices.Equal(points, wantPoints) {
		t.Fatalf("crash points = %v, want %v", points, wantPoints)
	}

	t.Run("committed", testPromoteCrashAfterCommit)

	for i, at := range points {
		name := string(at.step) + "/" + strings.TrimSuffix(at.path, filepath.Ext(at.path))
		started := at.step == checkpoint.StepApplying || at.step == checkpoint.StepTargetMutated
		crash := func(t *testing.T) *promoteCrashFixture {
			t.Helper()
			f := newPromoteCrashFixture(t)
			if err := f.run(t, i, nil); err != nil {
				t.Fatalf("crash at %s: %v", name, err)
			}
			return f
		}
		t.Run(name+"/settles", func(t *testing.T) { crash(t).assertSettlesAndRetries(t) })
		if !started {
			t.Run(name+"/slots-lost", func(t *testing.T) {
				f := crash(t)
				testutil.FailErr(t, "lose slots", os.RemoveAll(filepath.Join(f.txDir(), "retained")))
				f.assertSettlesAndRetries(t)
			})
			continue
		}
		t.Run(name+"/slot-missing-refused", func(t *testing.T) {
			f := crash(t)
			before := f.primarySnapshot(t)
			testutil.FailErr(t, "remove slot", os.Remove(f.slot("edited.sh")))
			if _, err := recoverPromoteTransaction(t.Context(), f.dataDir, f.task.ID, f.promote, f.task); err == nil {
				t.Fatal("recovered without the retained original")
			}
			if !maps.Equal(before, f.primarySnapshot(t)) {
				t.Fatal("refused recovery mutated a primary")
			}
		})
		t.Run(name+"/slot-replaced-refused", func(t *testing.T) {
			f := crash(t)
			before := f.primarySnapshot(t)
			testutil.FailErr(t, "remove slot", os.Remove(f.slot("removed.txt")))
			testutil.FailErr(t, "replace slot", os.WriteFile(f.slot("removed.txt"), []byte("old-removed\n"), 0o640))
			testutil.FailErr(t, "chmod replaced slot", os.Chmod(f.slot("removed.txt"), 0o640))
			if _, err := recoverPromoteTransaction(t.Context(), f.dataDir, f.task.ID, f.promote, f.task); err == nil {
				t.Fatal("recovered from a replaced retained original")
			}
			if !maps.Equal(before, f.primarySnapshot(t)) {
				t.Fatal("refused recovery mutated a primary")
			}
		})
		if at.step == checkpoint.StepTargetMutated {
			// Rollback itself may crash: restore one target as rollback would, then recover afresh.
			t.Run(name+"/recovery-crash", func(t *testing.T) {
				f := crash(t)
				testutil.FailErr(t, "clear edited", os.Remove(f.edited))
				testutil.FailErr(t, "relink edited", os.Link(f.slot("edited.sh"), f.edited))
				document := f.readJournalDocument(t)
				document["rolling_back"] = true
				f.writeJournalDocument(t, document)
				f.assertSettlesAndRetries(t)
			})
		}
	}
}

func TestPromoteTransactionCloseLeavesNoApplyingJournalWithoutSlots(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory permission this fault relies on")
	}
	f := newPromoteCrashFixture(t)
	tx, err := beginPromoteTransaction(f.dataDir, f.task.ID, f.plans)
	testutil.FailErr(t, "begin", err)
	testutil.FailErr(t, "apply", tx.apply(t.Context(), f.promote, f.task))
	retained := filepath.Join(f.txDir(), "retained")
	testutil.FailErr(t, "protect slots", os.Chmod(retained, 0o500))
	t.Cleanup(func() { _ = os.Chmod(retained, 0o700) })
	if err := tx.close(); err == nil {
		t.Fatal("expected slot removal failure to be reported")
	}
	if _, err := os.Lstat(f.journalPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("applying journal outlived a partial close: %v", err)
	}
	testutil.FailErr(t, "unprotect slots", os.Chmod(retained, 0o700))
	found, err := recoverPromoteTransaction(t.Context(), f.dataDir, f.task.ID, f.promote, f.task)
	if err != nil || found {
		t.Fatalf("recover after partial close = %v, %v", found, err)
	}
	// Inert slots carry no authority, so the next transaction starts clean.
	next, err := beginPromoteTransaction(f.dataDir, f.task.ID, nil)
	testutil.FailErr(t, "begin over inert slots", err)
	if _, err := os.Lstat(retained); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inert slots survived: %v", err)
	}
	testutil.FailErr(t, "close next", next.close())
}
