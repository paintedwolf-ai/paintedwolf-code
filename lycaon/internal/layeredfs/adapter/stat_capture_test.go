package adapter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/fileclone"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
)

// contentOpens records every path read through the content door for the length of a test.
type contentOpens struct {
	mu    sync.Mutex
	paths []string
}

func watchContentOpens(t *testing.T) *contentOpens {
	t.Helper()
	watched := &contentOpens{}
	saved := contentOpen
	contentOpen = func(path string) (*os.File, error) {
		watched.mu.Lock()
		watched.paths = append(watched.paths, path)
		watched.mu.Unlock()
		return saved(path)
	}
	t.Cleanup(func() { contentOpen = saved })
	return watched
}

func (c *contentOpens) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.paths
	c.paths = nil
	return out
}

// copyingClone stands in for a clone on any filesystem. It reads around the content door, as a real
// clone reads no content at all.
func copyingClone(src, dst string) (bool, error) {
	raw, err := os.ReadFile(src)
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(dst, raw, 0o600)
}

// agedTree writes files whose mtimes are far from any manifest, so only a real change is a candidate.
func agedTree(t *testing.T, count int) string {
	t.Helper()
	root := t.TempDir()
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(root, "dir"), 0o755))
	old := time.Now().Add(-time.Hour)
	for i := range count {
		path := filepath.Join(root, "dir", fmt.Sprintf("f%02d.txt", i))
		testutil.FailErr(t, "write", os.WriteFile(path, []byte(fmt.Sprintf("content %02d\n", i)), 0o644))
		testutil.FailErr(t, "age", os.Chtimes(path, old, old))
	}
	return root
}

func prepareClonedView(t *testing.T, viewID, source string) (*EagerCopyAdapter, *PreparedView) {
	t.Helper()
	adp := NewEagerCopyAdapter(ReasonNone)
	view, err := adp.Prepare(t.Context(), PrepareRequest{
		ViewID: viewID, RuntimeDir: t.TempDir(), CloneFile: copyingClone,
		Grants: []RootGrant{{RootID: "repo", Label: "repo", SourcePath: source, IsPrimary: true}},
	})
	testutil.FailErr(t, "Prepare", err)
	t.Cleanup(func() { _, _ = adp.Release(context.Background(), viewID) })
	return adp, view
}

func captureView(t *testing.T, adp *EagerCopyAdapter, viewID string) {
	t.Helper()
	_, release, err := adp.CheckpointBarrier(t.Context(), viewID)
	testutil.FailErr(t, "CheckpointBarrier", err)
	testutil.FailErr(t, "release", release())
}

func TestClonePreparationReadsNoFileContent(t *testing.T) {
	watched := watchContentOpens(t)
	source := agedTree(t, 8)
	_, view := prepareClonedView(t, "clone-reads-nothing", source)
	if opened := watched.take(); len(opened) != 0 {
		t.Fatalf("a clone preparation opened content: %v", opened)
	}
	stats := view.Materialize
	// Each of the 8 files is cloned into both trees.
	if stats.ClonedFiles != 16 || stats.CopiedFiles != 0 || stats.CopiedBytes != 0 || stats.ContentBytesRead != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	if _, captured := view.Core.CapturedBase(layeredfs.QualifiedPath{RootID: "repo", Rel: "dir/f00.txt"}); captured {
		t.Fatal("preparation hashed a base")
	}
}

func TestRealClonePreparationReadsNoFileContent(t *testing.T) {
	source := agedTree(t, 3)
	work := t.TempDir()
	supported, err := fileclone.SupportedFrom(t.Context(), filepath.Join(source, "dir", "f00.txt"), work)
	testutil.FailErr(t, "probe clone", err)
	if !supported {
		t.Skip("this volume cannot clone")
	}
	watched := watchContentOpens(t)
	adp := NewEagerCopyAdapter(ReasonNone)
	view, err := adp.Prepare(t.Context(), PrepareRequest{
		ViewID: "real-clone", RuntimeDir: t.TempDir(), TargetRootDirs: map[string]string{"repo": filepath.Join(work, "tree")},
		PristineDir: filepath.Join(work, "pristine"), BackingDir: filepath.Join(work, "backing"),
		Grants: []RootGrant{{RootID: "repo", SourcePath: source, IsPrimary: true}},
	})
	testutil.FailErr(t, "Prepare", err)
	t.Cleanup(func() { _, _ = adp.Release(context.Background(), "real-clone") })
	if opened := watched.take(); len(opened) != 0 || view.Materialize.ClonedFiles != 6 {
		t.Fatalf("opened %v, stats %+v", opened, view.Materialize)
	}
}

func TestAByteCopyHoldsItsBytesTwiceAndCountsThem(t *testing.T) {
	source := agedTree(t, 2)
	adp := NewEagerCopyAdapter(ReasonMountDenied)
	view, err := adp.Prepare(t.Context(), PrepareRequest{
		ViewID: "byte-copy", RuntimeDir: t.TempDir(), CloneFile: func(string, string) (bool, error) { return false, nil },
		Grants: []RootGrant{{RootID: "repo", SourcePath: source, IsPrimary: true}},
	})
	testutil.FailErr(t, "Prepare", err)
	t.Cleanup(func() { _, _ = adp.Release(context.Background(), "byte-copy") })
	size := int64(len("content 00\n"))
	if got := view.Materialize; got.CopiedFiles != 4 || got.CopiedBytes != 4*size || got.ClonedFiles != 0 {
		t.Fatalf("stats = %+v", got)
	}
}

func TestPristineTreeIsAnExactReadOnlyBase(t *testing.T) {
	source := agedTree(t, 1)
	_, view := prepareClonedView(t, "pristine-tree", source)
	pristine := filepath.Join(view.Roots[0].PristinePath, "dir", "f00.txt")
	info, err := os.Lstat(pristine)
	testutil.FailErr(t, "stat pristine", err)
	if info.Mode().Perm() != 0o400 {
		t.Fatalf("pristine mode = %v", info.Mode())
	}
	raw, err := os.ReadFile(pristine)
	testutil.FailErr(t, "read pristine", err)
	if string(raw) != "content 00\n" {
		t.Fatalf("pristine bytes = %q", raw)
	}
	testutil.FailErr(t, "worker edit", os.WriteFile(filepath.Join(view.PrimaryWorkPath, "dir", "f00.txt"), []byte("edited\n"), 0o644))
	if raw, _ := os.ReadFile(pristine); string(raw) != "content 00\n" {
		t.Fatalf("a working edit reached the base: %q", raw)
	}
}

func TestCaptureNeverReadsUnchangedFiles(t *testing.T) {
	source := agedTree(t, 8)
	adp, view := prepareClonedView(t, "stat-capture", source)
	work := filepath.Join(view.PrimaryWorkPath, "dir")
	pristine := filepath.Join(view.Roots[0].PristinePath, "dir")
	testutil.FailErr(t, "edit", os.WriteFile(filepath.Join(work, "f01.txt"), []byte("an edit of another size\n"), 0o644))
	testutil.FailErr(t, "delete", os.Remove(filepath.Join(work, "f02.txt")))
	testutil.FailErr(t, "add", os.WriteFile(filepath.Join(work, "new.txt"), []byte("new\n"), 0o644))

	watched := watchContentOpens(t)
	captureView(t, adp, "stat-capture")
	opened := map[string]bool{}
	for _, p := range watched.take() {
		opened[p] = true
	}
	allowed := map[string]bool{
		filepath.Join(work, "f01.txt"): true, filepath.Join(pristine, "f01.txt"): true,
		filepath.Join(pristine, "f02.txt"): true, filepath.Join(work, "new.txt"): true,
	}
	for p := range opened {
		if !allowed[p] {
			t.Errorf("capture read a file whose stat did not change: %s", p)
		}
	}
	for p := range allowed {
		if !opened[p] {
			t.Errorf("capture never read %s", p)
		}
	}
	stats, ok := adp.LastCaptureStats("stat-capture")
	// The directory, the 8 original files less one, and the new one.
	if !ok || stats.EntriesWalked != 9 || stats.Removed != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	changes, err := view.Core.EnumerateChanges(true)
	testutil.FailErr(t, "EnumerateChanges", err)
	byPath := map[string]layeredfs.ChangeEntry{}
	for _, change := range changes {
		byPath[change.Path.Rel] = change
	}
	if len(byPath) != 3 || !byPath["dir/f02.txt"].Deleted || byPath["dir/new.txt"].Base.Kind != layeredfs.EntryAbsent {
		t.Fatalf("changes = %+v", changes)
	}
	baseBytes, err := os.ReadFile(byPath["dir/f01.txt"].Base.BackingFile)
	testutil.FailErr(t, "read held base", err)
	if string(baseBytes) != "content 01\n" || byPath["dir/f01.txt"].Base.SHA256 == "" {
		t.Fatalf("the base must come from the pristine tree: %q %+v", baseBytes, byPath["dir/f01.txt"].Base)
	}

	// A second capture of the same state reads nothing it already took in.
	watched.take()
	captureView(t, adp, "stat-capture")
	if again := watched.take(); len(again) != 0 && !allRacy(again, opened) {
		t.Fatalf("a repeat capture read %v", again)
	}
}

// allRacy accepts a re-read only of files the first capture just recorded, whose fresh mtimes the racy
// rule refuses to trust.
func allRacy(paths []string, recorded map[string]bool) bool {
	for _, p := range paths {
		if !recorded[p] {
			return false
		}
	}
	return true
}

func TestASameSizeEditRightAfterTheManifestIsFound(t *testing.T) {
	// A file written just now has an mtime inside the racy window of the manifest taken moments later.
	source := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(source, "fresh.txt"), []byte("before\n"), 0o644))
	adp, view := prepareClonedView(t, "racy-edit", source)
	path := filepath.Join(view.PrimaryWorkPath, "fresh.txt")
	info, err := os.Lstat(path)
	testutil.FailErr(t, "stat", err)
	testutil.FailErr(t, "same-size edit", os.WriteFile(path, []byte("after!\n"), 0o644))
	// Timestamp granularity can leave an edit with the mtime it found.
	testutil.FailErr(t, "restore mtime", os.Chtimes(path, info.ModTime(), info.ModTime()))

	captureView(t, adp, "racy-edit")
	changes, err := view.Core.EnumerateChanges(true)
	testutil.FailErr(t, "EnumerateChanges", err)
	if len(changes) != 1 || changes[0].Path.Rel != "fresh.txt" || !changes[0].ContentChanged {
		t.Fatalf("the same-size edit was missed: %+v", changes)
	}
}

func TestStatRecordRules(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	testutil.FailErr(t, "write", os.WriteFile(path, []byte("x"), 0o644))
	old := time.Now().Add(-time.Hour)
	testutil.FailErr(t, "age", os.Chtimes(path, old, old))
	info, err := os.Lstat(path)
	testutil.FailErr(t, "stat", err)
	rec := recordStat("r", "f", layeredfs.EntryRegular, info)
	now := time.Now().UnixNano()
	if !rec.unchangedBy(info, now) {
		t.Fatal("an aged file with an identical stat must count as unchanged")
	}
	if rec.unchangedBy(info, rec.MtimeNS+racyWindow.Nanoseconds()) {
		t.Fatal("an mtime inside the window of the manifest's time must count as changed")
	}
	for name, change := range map[string]func(*statRecord){
		"size":  func(r *statRecord) { r.Size++ },
		"mtime": func(r *statRecord) { r.MtimeNS++ },
		"ctime": func(r *statRecord) { r.CtimeNS++ },
		"inode": func(r *statRecord) { r.Ino++ },
		"mode":  func(r *statRecord) { r.Mode ^= 0o100 },
	} {
		altered := rec
		change(&altered)
		if altered.unchangedBy(info, now) && (name != "ctime" && name != "inode" || statIdentityOf(info) != statIdentity{}) {
			t.Fatalf("a different %s must count as changed", name)
		}
	}
}

func TestReopenReDerivesChangesFromTheManifest(t *testing.T) {
	source := agedTree(t, 4)
	first := NewEagerCopyAdapter(ReasonNone)
	base := t.TempDir()
	manifest := filepath.Join(base, "stat-manifest")
	view, err := first.Prepare(t.Context(), PrepareRequest{
		ViewID: "before-restart", RuntimeDir: filepath.Join(base, "rt"), BackingDir: filepath.Join(base, "backing"),
		StatManifestPath: manifest, PristineDir: filepath.Join(base, "pristine"), CloneFile: copyingClone,
		TargetRootDirs: map[string]string{"repo": filepath.Join(base, "tree")},
		Grants:         []RootGrant{{RootID: "repo", Label: "repo", SourcePath: source, IsPrimary: true}},
	})
	testutil.FailErr(t, "Prepare", err)
	testutil.FailErr(t, "edit", os.WriteFile(filepath.Join(view.PrimaryWorkPath, "dir", "f00.txt"), []byte("a worker's edit\n"), 0o644))
	testutil.FailErr(t, "delete", os.Remove(filepath.Join(view.PrimaryWorkPath, "dir", "f01.txt")))
	// The process that prepared the view is gone: nothing of it survives but the two trees and manifest.
	testutil.FailErr(t, "release core", view.Core.ReleaseView())
	sharedEagerViews.Delete("before-restart")

	watched := watchContentOpens(t)
	next := NewEagerCopyAdapter(ReasonNone)
	testutil.FailErr(t, "clear backing", os.RemoveAll(filepath.Join(base, "backing")))
	reopened, err := next.Reopen(t.Context(), ReopenRequest{
		ViewID: "before-restart", RuntimeDir: filepath.Join(base, "rt"), BackingDir: filepath.Join(base, "backing"), StatManifestPath: manifest,
	})
	testutil.FailErr(t, "Reopen", err)
	t.Cleanup(func() { _, _ = next.Release(context.Background(), "before-restart") })
	if opened := watched.take(); len(opened) != 0 {
		t.Fatalf("reopening read content: %v", opened)
	}
	captureView(t, next, "before-restart")
	for _, p := range watched.take() {
		if rel, err := filepath.Rel(reopened.PrimaryWorkPath, p); err == nil && rel == filepath.Join("dir", "f03.txt") {
			t.Fatalf("recovery read an unchanged file: %s", p)
		}
	}
	changes, err := reopened.Core.EnumerateChanges(true)
	testutil.FailErr(t, "EnumerateChanges", err)
	if len(changes) != 2 {
		t.Fatalf("recovery re-derived %+v", changes)
	}
}

func TestChangedPathsCompareWithTheBaseWithoutTouchingCore(t *testing.T) {
	source := agedTree(t, 3)
	adp, view := prepareClonedView(t, "listing", source)
	work := filepath.Join(view.PrimaryWorkPath, "dir")
	testutil.FailErr(t, "edit", os.WriteFile(filepath.Join(work, "f00.txt"), []byte("edited\n"), 0o644))
	testutil.FailErr(t, "add", os.WriteFile(filepath.Join(work, "added.txt"), []byte("added\n"), 0o644))
	testutil.FailErr(t, "delete", os.Remove(filepath.Join(work, "f01.txt")))
	want := []string{"dir/added.txt", "dir/f00.txt", "dir/f01.txt"}
	list := func() []string {
		paths, _, err := adp.ChangedPaths(t.Context(), "listing")
		testutil.FailErr(t, "ChangedPaths", err)
		out := make([]string, 0, len(paths))
		for _, p := range paths {
			out = append(out, p.Rel)
		}
		return out
	}
	if got := list(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("before capture: %v", got)
	}
	if changes, _ := view.Core.EnumerateChanges(true); len(changes) != 0 {
		t.Fatalf("listing changes reached Core: %+v", changes)
	}
	captureView(t, adp, "listing")
	if got := list(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("after capture: %v", got)
	}
	// A revert is no change, even though a capture took in the edit.
	raw, err := os.ReadFile(filepath.Join(view.Roots[0].PristinePath, "dir", "f00.txt"))
	testutil.FailErr(t, "read base", err)
	testutil.FailErr(t, "revert", os.WriteFile(filepath.Join(work, "f00.txt"), raw, 0o644))
	if got := list(); fmt.Sprint(got) != fmt.Sprint([]string{"dir/added.txt", "dir/f01.txt"}) {
		t.Fatalf("after revert: %v", got)
	}
}

func TestAnUnknownManifestFormatIsRefusedUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stat-manifest")
	raw := []byte(`{"format":99,"view_id":"v"}` + "\n")
	testutil.FailErr(t, "write", os.WriteFile(path, raw, 0o600))
	_, err := NewEagerCopyAdapter(ReasonNone).Reopen(t.Context(), ReopenRequest{ViewID: "v", RuntimeDir: t.TempDir(), StatManifestPath: path})
	if !errors.Is(err, ErrUnsupportedStatManifest) {
		t.Fatalf("err = %v", err)
	}
	after, err := os.ReadFile(path)
	testutil.FailErr(t, "read", err)
	if string(after) != string(raw) {
		t.Fatalf("refusal changed the manifest: %s", after)
	}
}

func TestNoCopyRunsPastItsBudget(t *testing.T) {
	source := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(source, "big.bin"), make([]byte, 1<<20), 0o644))
	watched := watchContentOpens(t)
	_, err := NewEagerCopyAdapter(ReasonNone).Prepare(t.Context(), PrepareRequest{
		ViewID: "bounded", RuntimeDir: t.TempDir(), CloneFile: func(string, string) (bool, error) { return false, nil },
		CopyBudget: &CopyBudget{Limit: 1 << 10, EntryCost: 1},
		Grants:     []RootGrant{{RootID: "repo", SourcePath: source, IsPrimary: true}},
	})
	var overrun *BudgetOverrunError
	if !errors.As(err, &overrun) {
		t.Fatalf("err = %v", err)
	}
	if opened := watched.take(); len(opened) != 0 {
		t.Fatalf("a copy started past its budget: %v", opened)
	}
}

func TestCancellationStopsACopyInsideAFile(t *testing.T) {
	source := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(source, "big.bin"), make([]byte, 1<<20), 0o644))
	target := filepath.Join(t.TempDir(), "tree")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err := NewEagerCopyAdapter(ReasonNone).Prepare(ctx, PrepareRequest{
		ViewID: "canceled-copy", RuntimeDir: t.TempDir(), TargetRootDirs: map[string]string{"repo": target},
		CloneFile:  func(string, string) (bool, error) { return false, nil },
		ProgressFn: func(MaterializeProgress) { cancel() },
		Grants:     []RootGrant{{RootID: "repo", SourcePath: source, IsPrimary: true}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("canceled tree remains: %v", statErr)
	}
}
