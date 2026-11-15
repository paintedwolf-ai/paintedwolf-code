package layeredfs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func setOSFileXattr(t *testing.T, path, attr string, val []byte) {
	t.Helper()
	err := unix.Setxattr(path, attr, val, 0)
	if err != nil {
		testutil.FailErr(t, "unix.Setxattr "+attr, err)
	}
}

// TestLowerTrunkXattrPreservedAndFaultInjected verifies Finding 1:
// real lower-file xattrs (macOS quarantine + user xattr on Darwin; supported user.* xattrs on Linux)
// are read from the trunk, preserved through copy-up into both BaseCapture and the private node,
// and non-ignorable metadata read errors are surfaced rather than discarded.
func TestLowerTrunkXattrPreservedAndFaultInjected(t *testing.T) {
	t.Parallel()

	t.Run("real_lower_xattr_and_quarantine_preserved_through_stat_and_copyup", func(t *testing.T) {
		t.Parallel()
		core, trunk, _ := newSingleRootTestCore(t)
		qp := QualifiedPath{RootID: "primary", Rel: "quarantined.txt"}
		trunkFile := filepath.Join(trunk, "quarantined.txt")
		testutil.FailErr(t, "write trunk file", os.WriteFile(trunkFile, []byte("original-payload\n"), 0o644))

		quarantineVal := []byte("0083;66300000;Safari;12345678-1234-1234-1234-1234567890AB")
		customVal := []byte("lower-custom-xattr-value")
		setOSFileXattr(t, trunkFile, platformPrimaryTestXattr, quarantineVal)
		setOSFileXattr(t, trunkFile, platformSecondaryTestXattr, customVal)

		gotQ, err := core.GetXattr(qp, platformPrimaryTestXattr, false)
		testutil.FailErr(t, "GetXattr primary on trunk", err)
		if !bytes.Equal(gotQ, quarantineVal) {
			t.Fatalf("expected primary xattr %q on trunk, got %q", string(quarantineVal), string(gotQ))
		}
		gotCustom, err := core.GetXattr(qp, platformSecondaryTestXattr, false)
		testutil.FailErr(t, "GetXattr secondary on trunk", err)
		if !bytes.Equal(gotCustom, customVal) {
			t.Fatalf("expected custom xattr %q on trunk, got %q", string(customVal), string(gotCustom))
		}
		names, err := core.ListXattr(qp, false)
		testutil.FailErr(t, "ListXattr on trunk", err)
		if len(names) < 2 {
			t.Fatalf("expected at least 2 xattrs on trunk, got %v", names)
		}

		hid, _, err := core.Open(qp, OpenReadWrite, 0)
		testutil.FailErr(t, "Open O_RDWR for physical copy-up", err)
		testutil.FailErr(t, "CloseHandle without write", core.CloseHandle(hid))

		logicalAfterTouch, err := core.EnumerateChanges(true)
		testutil.FailErr(t, "EnumerateChanges(true) after touch", err)
		if len(logicalAfterTouch) != 0 {
			t.Fatalf("expected 0 logical changes after no-op O_RDWR on xattr file, got %+v", logicalAfterTouch)
		}

		_, err = core.WriteFile(qp, []byte("mutated-payload\n"), 0o644)
		testutil.FailErr(t, "WriteFile mutated", err)

		base, ok := core.CapturedBase(qp)
		if !ok {
			t.Fatal("expected CapturedBase for quarantined.txt")
		}
		if !bytes.Equal(base.Xattrs[platformPrimaryTestXattr], quarantineVal) ||
			!bytes.Equal(base.Xattrs[platformSecondaryTestXattr], customVal) {
			t.Fatalf("expected CapturedBase to preserve lower xattrs, got %+v", base.Xattrs)
		}

		gotQAfter, err := core.GetXattr(qp, platformPrimaryTestXattr, false)
		testutil.FailErr(t, "GetXattr primary after copy-up", err)
		if !bytes.Equal(gotQAfter, quarantineVal) {
			t.Fatalf("expected primary xattr preserved after copy-up, got %q", string(gotQAfter))
		}
		gotCustomAfter, err := core.GetXattr(qp, platformSecondaryTestXattr, false)
		testutil.FailErr(t, "GetXattr secondary after copy-up", err)
		if !bytes.Equal(gotCustomAfter, customVal) {
			t.Fatalf("expected custom xattr preserved after copy-up, got %q", string(gotCustomAfter))
		}
	})

	t.Run("injected_xattr_read_failure_refuses_stat_and_copyup", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		trunk := filepath.Join(tmp, "trunk")
		backing := filepath.Join(tmp, "backing")
		testutil.FailErr(t, "mkdir trunk", os.MkdirAll(trunk, 0o755))
		testutil.FailErr(t, "mkdir backing", os.MkdirAll(backing, 0o700))

		trunkFile := filepath.Join(trunk, "fail_xattr.txt")
		testutil.FailErr(t, "write fail_xattr.txt", os.WriteFile(trunkFile, []byte("hello"), 0o644))
		setOSFileXattr(t, trunkFile, "user.test", []byte("val"))

		injectErr := false
		core, err := New(Config{
			ViewID:     "view-xattr-err",
			BackingDir: backing,
			Roots: []RootSpec{
				{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true},
			},
			FaultHooks: &FaultHooks{
				LowerXattrHook: func(qpath QualifiedPath) error {
					if injectErr && qpath.Rel == "fail_xattr.txt" {
						return syscall.EIO
					}
					return nil
				},
			},
		})
		testutil.FailErr(t, "New Core", err)
		defer func() { _ = core.ReleaseView() }()

		qp := QualifiedPath{RootID: "primary", Rel: "fail_xattr.txt"}
		injectErr = true

		if _, err := core.Stat(qp); !errors.Is(err, syscall.EIO) {
			t.Fatalf("expected syscall.EIO from Stat when ListXattr fails, got %v", err)
		}
		if _, err := core.WriteFile(qp, []byte("new"), 0o644); !errors.Is(err, syscall.EIO) {
			t.Fatalf("expected syscall.EIO from WriteFile copy-up when ListXattr fails, got %v", err)
		}
		if _, ok := core.CapturedBase(qp); ok {
			t.Fatal("expected no base capture published when ListXattr fails")
		}
	})
}

// TestXattrAncestorReplacementConfinement verifies Finding 2:
// lower ListXattr reads metadata strictly via held directory descriptors (Openat + Flistxattr/Fgetxattr),
// never reconstructing pathname strings from parent.Name().
func TestXattrAncestorReplacementConfinement(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	trunk := filepath.Join(tmp, "trunk")
	outside := filepath.Join(tmp, "outside")
	backing := filepath.Join(tmp, "backing")
	for _, d := range []string{filepath.Join(trunk, "sub"), outside, backing} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(d, 0o755))
	}

	insideFile := filepath.Join(trunk, "sub", "item.txt")
	outsideFile := filepath.Join(outside, "item.txt")
	testutil.FailErr(t, "write inside item.txt", os.WriteFile(insideFile, []byte("inside-bytes"), 0o644))
	testutil.FailErr(t, "write outside item.txt", os.WriteFile(outsideFile, []byte("outside-bytes"), 0o644))

	setOSFileXattr(t, insideFile, "user.origin", []byte("inside-authorized-root"))
	setOSFileXattr(t, outsideFile, "user.origin", []byte("OUTSIDE-FORBIDDEN-SECRET"))

	var swapParentOnce sync.Once
	core, err := New(Config{
		ViewID:     "view-xattr-confinement",
		BackingDir: backing,
		Roots: []RootSpec{
			{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true},
		},
		FaultHooks: &FaultHooks{
			LowerXattrHook: func(qpath QualifiedPath) error {
				if qpath.Rel == "sub/item.txt" {
					swapParentOnce.Do(func() {
						_ = os.Rename(filepath.Join(trunk, "sub"), filepath.Join(trunk, "sub_moved"))
						_ = os.Symlink(outside, filepath.Join(trunk, "sub"))
					})
				}
				return nil
			},
		},
	})
	testutil.FailErr(t, "New Core", err)
	defer func() { _ = core.ReleaseView() }()

	qp := QualifiedPath{RootID: "primary", Rel: "sub/item.txt"}
	attr, statErr := core.Stat(qp)
	if statErr == nil {
		if string(attr.Xattrs["user.origin"]) != "inside-authorized-root" {
			t.Fatalf("unexpected xattr value %q (outside secret leaked!)", string(attr.Xattrs["user.origin"]))
		}
	} else if !errors.Is(statErr, ErrSymlinkEscape) {
		t.Fatalf("expected either pinned inside xattr or ErrSymlinkEscape, got %v", statErr)
	}

	if _, err := core.Stat(qp); !errors.Is(err, ErrSymlinkEscape) {
		t.Fatalf("expected subsequent Stat after ancestor symlink replacement to fail with ErrSymlinkEscape, got %v", err)
	}
}

// TestConcurrentAppendWritersBothRecordsSurvive verifies Finding 3:
// two concurrent OpenAppend writers synchronized via a deterministic barrier (no sleeps)
// serialize their offset calculation + WriteAt + size/journal commit so both records survive.
func TestConcurrentAppendWritersBothRecordsSurvive(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	trunk := filepath.Join(tmp, "trunk")
	backing := filepath.Join(tmp, "backing")
	testutil.FailErr(t, "mkdir trunk", os.MkdirAll(trunk, 0o755))
	testutil.FailErr(t, "mkdir backing", os.MkdirAll(backing, 0o700))

	var bothAdmitted sync.WaitGroup
	bothAdmitted.Add(2)

	core, err := New(Config{
		ViewID:     "view-append-race",
		BackingDir: backing,
		Roots: []RootSpec{
			{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true},
		},
		FaultHooks: &FaultHooks{
			BeforeAppendLock: func(qpath QualifiedPath, _ []byte) {
				if qpath.Rel == "append.log" {
					bothAdmitted.Done()
					bothAdmitted.Wait()
				}
			},
		},
	})
	testutil.FailErr(t, "New Core", err)
	defer func() { _ = core.ReleaseView() }()

	qp := QualifiedPath{RootID: "primary", Rel: "append.log"}
	h1, _, err := core.Open(qp, OpenWriteOnly|OpenCreate|OpenAppend, 0o644)
	testutil.FailErr(t, "open handle 1 OpenAppend", err)
	h2, _, err := core.Open(qp, OpenWriteOnly|OpenAppend, 0o644)
	testutil.FailErr(t, "open handle 2 OpenAppend", err)

	recA := []byte("record-alpha-11111111\n")
	recB := []byte("record-beta-222222222\n")

	var wg sync.WaitGroup
	wg.Add(2)
	var errA, errB error
	var nA, nB int
	go func() {
		defer wg.Done()
		nA, errA = core.WriteHandle(h1, recA, 0)
	}()
	go func() {
		defer wg.Done()
		nB, errB = core.WriteHandle(h2, recB, 0)
	}()
	wg.Wait()

	testutil.FailErr(t, "append write A", errA)
	testutil.FailErr(t, "append write B", errB)
	if nA != len(recA) || nB != len(recB) {
		t.Fatalf("unexpected write counts nA=%d nB=%d", nA, nB)
	}
	testutil.FailErr(t, "close h1", core.CloseHandle(h1))
	testutil.FailErr(t, "close h2", core.CloseHandle(h2))

	gotBytes, err := core.ReadFile(qp)
	testutil.FailErr(t, "ReadFile append.log", err)
	wantLen := len(recA) + len(recB)
	if len(gotBytes) != wantLen {
		t.Fatalf("expected append.log length %d, got %d (%q)", wantLen, len(gotBytes), string(gotBytes))
	}
	gotStr := string(gotBytes)
	if !strings.Contains(gotStr, string(recA)) || !strings.Contains(gotStr, string(recB)) {
		t.Fatalf("both appended records must survive; got %q", gotStr)
	}
}

// TestPartialWriteFailureUpdatesAccountingAndJournal verifies Finding 4:
// when a write fails partway through (n > 0 && err != nil, e.g. ENOSPC), the actual
// partial byte mutation is recorded in node.size, contentChanged, and the mutation journal.
func TestPartialWriteFailureUpdatesAccountingAndJournal(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	trunk := filepath.Join(tmp, "trunk")
	backing := filepath.Join(tmp, "backing")
	testutil.FailErr(t, "mkdir trunk", os.MkdirAll(trunk, 0o755))
	testutil.FailErr(t, "mkdir backing", os.MkdirAll(backing, 0o700))

	testutil.FailErr(t, "write trunk seed.txt", os.WriteFile(filepath.Join(trunk, "seed.txt"), []byte("0123456789"), 0o644))

	core, err := New(Config{
		ViewID:     "view-partial-write",
		BackingDir: backing,
		Roots: []RootSpec{
			{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true},
		},
		FaultHooks: &FaultHooks{
			WriteAtFunc: func(qpath QualifiedPath, file *os.File, data []byte, offset int64) (int, error) {
				if qpath.Rel == "seed.txt" || qpath.Rel == "loose_ro" {
					half := len(data) / 2
					n, _ := file.WriteAt(data[:half], offset)
					return n, syscall.ENOSPC
				}
				return file.WriteAt(data, offset)
			},
		},
	})
	testutil.FailErr(t, "New Core", err)
	defer func() { _ = core.ReleaseView() }()

	qp := QualifiedPath{RootID: "primary", Rel: "seed.txt"}
	hid, _, err := core.Open(qp, OpenReadWrite, 0)
	testutil.FailErr(t, "Open seed.txt", err)

	payload := []byte("ABCDEFGHIJKL")
	n, wErr := core.WriteHandle(hid, payload, 10)
	if !errors.Is(wErr, syscall.ENOSPC) || n != 6 {
		t.Fatalf("expected (6, ENOSPC) from partial WriteHandle, got (%d, %v)", n, wErr)
	}
	testutil.FailErr(t, "CloseHandle", core.CloseHandle(hid))

	gotBytes, err := core.ReadFile(qp)
	testutil.FailErr(t, "ReadFile after partial write", err)
	if string(gotBytes) != "0123456789ABCDEF" {
		t.Fatalf("expected partial write bytes %q, got %q", "0123456789ABCDEF", string(gotBytes))
	}

	changes, err := core.EnumerateChanges(true)
	testutil.FailErr(t, "EnumerateChanges(true)", err)
	if len(changes) != 1 {
		t.Fatalf("expected 1 logical change after partial write, got %+v", changes)
	}
	wantSum := sha256.Sum256(gotBytes)
	if !changes[0].LogicalChange || !changes[0].ContentChanged || changes[0].Final.Size != 16 ||
		changes[0].Final.SHA256 != hex.EncodeToString(wantSum[:]) {
		t.Fatalf("unexpected ChangeEntry after partial write: %+v", changes[0])
	}

	roQP := QualifiedPath{RootID: "primary", Rel: "loose_ro"}
	roHID, _, err := core.CreateFile(roQP, 0o444, true)
	testutil.FailErr(t, "CreateFile loose_ro", err)
	nNode, nwErr := core.WriteHandle(roHID, []byte("12345678"), 0)
	if !errors.Is(nwErr, syscall.ENOSPC) || nNode != 4 {
		t.Fatalf("expected (4, ENOSPC) from partial WriteHandle, got (n=%d, err=%v)", nNode, nwErr)
	}
	updatedAttr, sErr := core.StatHandle(roHID)
	testutil.FailErr(t, "StatHandle loose_ro", sErr)
	if updatedAttr.Size != 4 {
		t.Fatalf("expected size 4 after partial write, got %d", updatedAttr.Size)
	}
	testutil.FailErr(t, "CloseHandle loose_ro", core.CloseHandle(roHID))
	snap, err := core.SnapshotJournal()
	testutil.FailErr(t, "SnapshotJournal", err)
	if len(snap.Changes) != 2 {
		t.Fatalf("expected 2 changes in snapshot after partial writes, got %d", len(snap.Changes))
	}
}
