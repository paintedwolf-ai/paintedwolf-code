package layeredfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type logFixture struct {
	trunk, backing string
}

func newLogFixture(t *testing.T, seed map[string]string) logFixture {
	t.Helper()
	tmp := t.TempDir()
	f := logFixture{trunk: filepath.Join(tmp, "trunk"), backing: filepath.Join(tmp, "backing")}
	testutil.FailErr(t, "mkdir trunk", os.MkdirAll(f.trunk, 0o755))
	for rel, body := range seed {
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(f.trunk, filepath.Dir(rel)), 0o755))
		testutil.FailErr(t, "seed "+rel, os.WriteFile(filepath.Join(f.trunk, rel), []byte(body), 0o644))
	}
	return f
}

func (f logFixture) config() Config {
	return Config{
		ViewID: "view-log", BackingDir: f.backing,
		Roots:        []RootSpec{{ID: "primary", Label: "primary", TrunkPath: f.trunk, IsPrimary: true}},
		FoldingProbe: func(string) NameFolding { return NameFolding{} },
	}
}

func (f logFixture) open(t *testing.T) *Core {
	t.Helper()
	c, err := New(f.config())
	testutil.FailErr(t, "New", err)
	return c
}

// exit drops a core the way an engine exit does: nothing is snapshotted or flushed.
func exit(c *Core) {
	c.closeWorkingLog()
	c.roots.close()
	c.backing.close()
}

func (f logFixture) resume(t *testing.T) (*Core, ResumeReport) {
	t.Helper()
	c, report, err := Resume(f.config())
	testutil.FailErr(t, "Resume", err)
	t.Cleanup(func() { _ = c.ReleaseView() })
	return c, report
}

// tree is everything a worker can observe under the root: each path's kind, mode, content or
// target, and attributes.
func tree(t *testing.T, c *Core) map[string]string {
	t.Helper()
	out := map[string]string{}
	var walk func(rel string)
	walk = func(rel string) {
		entries, err := c.ReadDir(primary(rel))
		testutil.FailErr(t, "ReadDir "+rel, err)
		for _, e := range entries {
			child := e.Name
			if rel != "." {
				child = rel + "/" + e.Name
			}
			attr, err := c.Lstat(primary(child))
			testutil.FailErr(t, "Lstat "+child, err)
			desc := fmt.Sprintf("%s %v %v", attr.Kind, attr.Mode, attr.Xattrs)
			switch attr.Kind {
			case EntryRegular:
				body, err := c.ReadFile(primary(child))
				testutil.FailErr(t, "ReadFile "+child, err)
				desc += " " + string(body)
			case EntrySymlink:
				desc += " -> " + attr.LinkTarget
			case EntryDirectory:
				walk(child)
			}
			out[child] = desc
		}
	}
	walk(".")
	return out
}

func changes(t *testing.T, c *Core) []string {
	t.Helper()
	entries, err := c.EnumerateChanges(true)
	testutil.FailErr(t, "EnumerateChanges", err)
	var out []string
	for _, e := range entries {
		out = append(out, fmt.Sprintf("%s %s deleted=%v content=%v", e.Path.Rel, e.Kind, e.Deleted, e.ContentChanged))
	}
	slices.Sort(out)
	return out
}

func requireSameView(t *testing.T, label string, want, got *Core) {
	t.Helper()
	if w, g := tree(t, want), tree(t, got); !maps(w, g) {
		t.Fatalf("%s: resumed tree\n%v\nwant\n%v", label, g, w)
	}
	if w, g := changes(t, want), changes(t, got); !slices.Equal(w, g) {
		t.Fatalf("%s: resumed changes\n%v\nwant\n%v", label, g, w)
	}
}

func maps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// workerSteps creates, renames, renames over, deletes and changes metadata, with no barrier.
func workerSteps(t *testing.T, c *Core) {
	t.Helper()
	must := func(label string, err error) { t.Helper(); testutil.FailErr(t, label, err) }
	_, err := c.WriteFile(primary("new.txt"), []byte("new\n"), 0o644)
	must("create", err)
	_, err = c.Mkdir(primary("dir"), 0o755)
	must("mkdir", err)
	_, err = c.WriteFile(primary("dir/inner.txt"), []byte("inner\n"), 0o644)
	must("create inner", err)
	must("rename file", c.Rename(primary("new.txt"), primary("dir/moved.txt")))
	must("rename over", c.Rename(primary("dir/inner.txt"), primary("keep.txt")))
	must("rename dir", c.Rename(primary("dir"), primary("renamed")))
	must("rename trunk dir", c.Rename(primary("src"), primary("lib")))
	must("delete trunk file", c.Unlink(primary("gone.txt"), false))
	_, err = c.Symlink("keep.txt", primary("link"))
	must("symlink", err)
	_, err = c.Link(primary("renamed/moved.txt"), primary("hard.txt"))
	must("hard link", err)
	_, err = c.Chmod(primary("lib/a.go"), 0o600)
	must("chmod", err)
	must("xattr", c.SetXattr(primary("keep.txt"), "user.note", []byte("x"), true))
	_, err = c.WriteFile(primary("lib/b.go"), []byte("package b // changed\n"), 0o644)
	must("rewrite", err)
}

var workerSeed = map[string]string{"keep.txt": "trunk keep\n", "gone.txt": "gone\n", "src/a.go": "package a\n", "src/b.go": "package b\n"}

// An engine that exits with no barrier leaves a namespace its next start restores exactly.
func TestResumeRestoresTheNamespaceAfterAnEngineExit(t *testing.T) {
	f := newLogFixture(t, workerSeed)
	c := f.open(t)
	workerSteps(t, c)
	if flushes := c.Stats().Flushes; flushes != 0 {
		t.Fatalf("the steps flushed %d times", flushes)
	}
	want := tree(t, c)
	wantChanges := changes(t, c)
	exit(c)

	resumed, report := f.resume(t)
	if report.TruncatedBytes != 0 || report.Records == 0 {
		t.Fatalf("report %+v", report)
	}
	if got := tree(t, resumed); !maps(got, want) {
		t.Fatalf("resumed tree\n%v\nwant\n%v", got, want)
	}
	if got := changes(t, resumed); !slices.Equal(got, wantChanges) {
		t.Fatalf("resumed changes\n%v\nwant\n%v", got, wantChanges)
	}

	// The resumed view keeps recording, so a second exit loses nothing either.
	testutil.FailErr(t, "rename after resume", resumed.Rename(primary("keep.txt"), primary("kept.txt")))
	again, _, err := Resume(f.config())
	testutil.FailErr(t, "resume again", err)
	defer func() { _ = again.ReleaseView() }()
	requireSameView(t, "second resume", resumed, again)
}

// Each mutation appends at most one record, and a record's size depends on what the mutation
// touched, not on the size of the tree beneath it.
func TestWorkingLogCostsOneRecordPerMutationAtAnyTreeSize(t *testing.T) {
	recordBytes := func(files int) int64 {
		seed := map[string]string{"target.txt": "t\n"}
		for i := range files {
			seed[fmt.Sprintf("bulk/f%04d.txt", i)] = "x\n"
		}
		f := newLogFixture(t, seed)
		c := f.open(t)
		defer func() { _ = c.ReleaseView() }()
		before := c.Stats().WorkingLogRecords
		testutil.FailErr(t, "rename", c.Rename(primary("target.txt"), primary("renamed.txt")))
		if got := c.Stats().WorkingLogRecords - before; got != 1 {
			t.Fatalf("a rename appended %d records", got)
		}
		info, err := os.Stat(filepath.Join(f.backing, "journal", workingLogName))
		testutil.FailErr(t, "stat log", err)
		return info.Size()
	}
	// The bound leaves room for the paths and timestamps one rename records, not for a listing.
	const recordBound = 4 << 10
	if small, large := recordBytes(10), recordBytes(1000); small > recordBound || large > recordBound {
		t.Fatalf("a rename logged %d bytes over 10 files and %d over 1000", small, large)
	}

	f := newLogFixture(t, nil)
	c := f.open(t)
	defer func() { _ = c.ReleaseView() }()
	h, _, err := c.Open(primary("data.bin"), OpenReadWrite|OpenCreate, 0o644)
	testutil.FailErr(t, "create", err)
	before := c.Stats().WorkingLogRecords
	for i := range 50 {
		_, err := c.WriteHandle(h, []byte("chunk"), int64(i*5))
		testutil.FailErr(t, "write", err)
	}
	if got := c.Stats().WorkingLogRecords - before; got != 0 {
		t.Fatalf("50 writes appended %d records", got)
	}
	testutil.FailErr(t, "close", c.CloseHandle(h))
}

// A torn final record is dropped, every record before it applies, and the loss is reported.
func TestResumeStopsBeforeATornRecordAndReportsIt(t *testing.T) {
	f := newLogFixture(t, workerSeed)
	c := f.open(t)
	_, err := c.WriteFile(primary("first.txt"), []byte("1\n"), 0o644)
	testutil.FailErr(t, "first", err)
	logPath := filepath.Join(f.backing, "journal", workingLogName)
	info, err := os.Stat(logPath)
	testutil.FailErr(t, "stat", err)
	complete := info.Size()
	testutil.FailErr(t, "second", c.Rename(primary("first.txt"), primary("second.txt")))
	exit(c)
	info, err = os.Stat(logPath)
	testutil.FailErr(t, "stat", err)
	testutil.FailErr(t, "tear", os.Truncate(logPath, complete+(info.Size()-complete)/2))

	resumed, report := f.resume(t)
	if report.TruncatedBytes != (info.Size()-complete)/2 {
		t.Fatalf("report %+v", report)
	}
	if _, err := resumed.Stat(primary("first.txt")); err != nil {
		t.Fatalf("the complete record was lost: %v", err)
	}
	if _, err := resumed.Stat(primary("second.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the torn rename applied: %v", err)
	}
	// Recording resumes after the last complete record.
	testutil.FailErr(t, "rename after resume", resumed.Rename(primary("first.txt"), primary("third.txt")))
	again, _, err := Resume(f.config())
	testutil.FailErr(t, "resume again", err)
	defer func() { _ = again.ReleaseView() }()
	requireSameView(t, "after torn tail", resumed, again)
}

// Compaction folds the log into a snapshot, and resuming applies the snapshot and the records after it.
func TestResumeAppliesTheSnapshotAndTheRecordsAfterIt(t *testing.T) {
	f := newLogFixture(t, workerSeed)
	c := f.open(t)
	workerSteps(t, c)
	c.wal.mu.Lock()
	c.mu.Lock()
	snap := c.snapshotLocked()
	c.mu.Unlock()
	testutil.FailErr(t, "snapshot", c.writeSnapshotWAL(snap))
	c.wal.mu.Unlock()
	testutil.FailErr(t, "rename after snapshot", c.Rename(primary("keep.txt"), primary("after.txt")))
	want, wantChanges := tree(t, c), changes(t, c)
	exit(c)

	resumed, report := f.resume(t)
	if !report.FromSnapshot || report.Records != 1 {
		t.Fatalf("report %+v", report)
	}
	if got := tree(t, resumed); !maps(got, want) {
		t.Fatalf("resumed tree\n%v\nwant\n%v", got, want)
	}
	if got := changes(t, resumed); !slices.Equal(got, wantChanges) {
		t.Fatalf("resumed changes\n%v\nwant\n%v", got, wantChanges)
	}
}

// Working storage with no log is not resumed as an empty view.
func TestResumeRefusesStorageWithoutALog(t *testing.T) {
	f := newLogFixture(t, workerSeed)
	c := f.open(t)
	_, err := c.WriteFile(primary("new.txt"), []byte("new\n"), 0o644)
	testutil.FailErr(t, "create", err)
	exit(c)
	testutil.FailErr(t, "remove log", os.Remove(filepath.Join(f.backing, "journal", workingLogName)))
	if _, _, err := Resume(f.config()); !errors.Is(err, ErrWorkingStateUnusable) {
		t.Fatalf("resume without a log: %v", err)
	}
}
