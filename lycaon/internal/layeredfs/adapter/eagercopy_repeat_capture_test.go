package adapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"golang.org/x/sys/unix"
)

func TestEagerCopyRepeatCaptureModifyRevertAndCreateDelete(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	srcRoot := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	seedContentA := []byte("version-A\n")
	seedContentB := []byte("version-B-modified\n")
	chmodContent := []byte("#!/bin/sh\necho ok\n")
	untouchedContent := []byte("stay-put\n")

	seedFile := filepath.Join(srcRoot, "seeded.txt")
	chmodFile := filepath.Join(srcRoot, "chmod_revert.sh")
	untouchedFile := filepath.Join(srcRoot, "untouched.txt")
	if err := os.WriteFile(seedFile, seedContentA, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chmodFile, chmodContent, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(untouchedFile, untouchedContent, 0o644); err != nil {
		t.Fatal(err)
	}
	xattrSupported := unix.Lsetxattr(seedFile, "user.seed", []byte("orig"), 0) == nil

	adp := NewEagerCopyAdapter(ReasonMountDenied)
	view, err := adp.Prepare(ctx, PrepareRequest{
		ViewID:     "repeat-capture-1",
		RuntimeDir: filepath.Join(tmp, "rt"),
		Grants:     []RootGrant{{RootID: "repo", Label: "repo", SourcePath: srcRoot, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	defer func() { _, _ = adp.Release(ctx, view.ViewID) }()

	workSeeded := filepath.Join(view.PrimaryWorkPath, "seeded.txt")
	workChmod := filepath.Join(view.PrimaryWorkPath, "chmod_revert.sh")
	workEphemeral := filepath.Join(view.PrimaryWorkPath, "ephemeral.txt")
	workEphemeralDir := filepath.Join(view.PrimaryWorkPath, "ephemeral-dir")

	// Capture #1: modify seeded.txt (content + mode + xattrs), chmod chmod_revert.sh, create ephemeral file & dir.
	if err := os.WriteFile(workSeeded, seedContentB, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(workSeeded, 0o755); err != nil {
		t.Fatal(err)
	}
	if xattrSupported {
		if err := unix.Lsetxattr(workSeeded, "user.seed", []byte("mutated"), 0); err != nil {
			t.Fatal(err)
		}
		if err := unix.Lsetxattr(workSeeded, "user.extra", []byte("temp"), 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(workChmod, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workEphemeral, []byte("temp-created\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workEphemeralDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workEphemeralDir, "nested.txt"), []byte("nested\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, release1, err := adp.CheckpointBarrier(ctx, view.ViewID)
	if err != nil {
		t.Fatalf("CheckpointBarrier #1 failed: %v", err)
	}
	assertCapture1State(t, view, seedContentA, seedContentB, chmodContent, xattrSupported)
	_ = release1()

	// Capture #2: revert seeded.txt back to seed state, revert chmod_revert.sh mode, delete ephemeral file & dir.
	if err := os.WriteFile(workSeeded, seedContentA, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(workSeeded, 0o644); err != nil {
		t.Fatal(err)
	}
	if xattrSupported {
		if err := unix.Lsetxattr(workSeeded, "user.seed", []byte("orig"), 0); err != nil {
			t.Fatal(err)
		}
		if err := unix.Lremovexattr(workSeeded, "user.extra"); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(workChmod, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(workEphemeral); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(workEphemeralDir); err != nil {
		t.Fatal(err)
	}

	_, release2, err := adp.CheckpointBarrier(ctx, view.ViewID)
	if err != nil {
		t.Fatalf("CheckpointBarrier #2 failed: %v", err)
	}
	assertCapture2RevertedAndDeletedState(t, view, seedContentA, chmodContent, xattrSupported)
	_ = release2()
}

func assertCapture1State(t *testing.T, view *PreparedView, seedA, seedB, chmodBytes []byte, xattrSupported bool) {
	t.Helper()
	qpSeeded := layeredfs.QualifiedPath{RootID: "repo", Rel: "seeded.txt"}
	qpChmod := layeredfs.QualifiedPath{RootID: "repo", Rel: "chmod_revert.sh"}
	qpEphem := layeredfs.QualifiedPath{RootID: "repo", Rel: "ephemeral.txt"}

	gotSeeded, err := view.Core.ReadFile(qpSeeded)
	if err != nil || !bytes.Equal(gotSeeded, seedB) {
		t.Fatalf("capture #1 seeded.txt mismatch: err=%v got=%q", err, string(gotSeeded))
	}
	gotChmod, err := view.Core.ReadFile(qpChmod)
	if err != nil || !bytes.Equal(gotChmod, chmodBytes) {
		t.Fatalf("capture #1 chmod_revert.sh content mismatch: err=%v got=%q", err, string(gotChmod))
	}
	gotEphem, err := view.Core.ReadFile(qpEphem)
	if err != nil || string(gotEphem) != "temp-created\n" {
		t.Fatalf("capture #1 ephemeral.txt mismatch: err=%v got=%q", err, string(gotEphem))
	}
	if xattrSupported {
		val, err := view.Core.GetXattr(qpSeeded, "user.seed", true)
		if err != nil || string(val) != "mutated" {
			t.Fatalf("capture #1 expected user.seed=mutated, err=%v val=%q", err, string(val))
		}
	}
	changes, err := view.Core.EnumerateChanges(true)
	if err != nil {
		t.Fatalf("EnumerateChanges #1 failed: %v", err)
	}
	sumA := sha256.Sum256(seedA)
	shaA := hex.EncodeToString(sumA[:])
	foundSeeded, foundChmod, foundEphem := false, false, false
	for _, ch := range changes {
		switch ch.Path.Rel {
		case "seeded.txt":
			foundSeeded = true
			if ch.Base.SHA256 != shaA || !ch.ContentChanged || !ch.MetadataChanged {
				t.Fatalf("unexpected seeded.txt change entry on capture #1: %+v", ch)
			}
		case "chmod_revert.sh":
			foundChmod = true
			if ch.ContentChanged || !ch.MetadataChanged {
				t.Fatalf("expected metadata-only change for chmod_revert.sh on capture #1, got %+v", ch)
			}
		case "ephemeral.txt":
			foundEphem = true
		case "untouched.txt":
			t.Fatalf("untouched.txt must not appear in logical changes: %+v", ch)
		}
	}
	if !foundSeeded || !foundChmod || !foundEphem {
		t.Fatalf("missing expected changes in capture #1: seeded=%v chmod=%v ephem=%v", foundSeeded, foundChmod, foundEphem)
	}
}

func assertCapture2RevertedAndDeletedState(t *testing.T, view *PreparedView, seedA, chmodBytes []byte, xattrSupported bool) {
	t.Helper()
	qpSeeded := layeredfs.QualifiedPath{RootID: "repo", Rel: "seeded.txt"}
	qpChmod := layeredfs.QualifiedPath{RootID: "repo", Rel: "chmod_revert.sh"}
	qpEphem := layeredfs.QualifiedPath{RootID: "repo", Rel: "ephemeral.txt"}
	qpEphemDir := layeredfs.QualifiedPath{RootID: "repo", Rel: "ephemeral-dir"}

	gotSeeded, err := view.Core.ReadFile(qpSeeded)
	if err != nil || !bytes.Equal(gotSeeded, seedA) {
		t.Fatalf("capture #2 expected reverted content A, err=%v got=%q", err, string(gotSeeded))
	}
	attrSeeded, err := view.Core.Lstat(qpSeeded)
	if err != nil || attrSeeded.Mode.Perm() != 0o644 {
		t.Fatalf("capture #2 expected reverted mode 0644 on seeded.txt, err=%v mode=%v", err, attrSeeded.Mode)
	}
	if xattrSupported {
		val, err := view.Core.GetXattr(qpSeeded, "user.seed", true)
		if err != nil || string(val) != "orig" {
			t.Fatalf("capture #2 expected reverted xattr user.seed=orig, err=%v val=%q", err, string(val))
		}
		if _, err := view.Core.GetXattr(qpSeeded, "user.extra", true); !errors.Is(err, syscall.ENODATA) {
			t.Fatalf("capture #2 expected user.extra removed, got err=%v", err)
		}
	}
	gotChmod, err := view.Core.ReadFile(qpChmod)
	if err != nil || !bytes.Equal(gotChmod, chmodBytes) {
		t.Fatalf("capture #2 chmod_revert.sh content mismatch: err=%v got=%q", err, string(gotChmod))
	}
	if _, err := view.Core.ReadFile(qpEphem); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("capture #2 expected created-then-deleted file absent from Core, got err=%v", err)
	}
	if _, err := view.Core.Lstat(qpEphemDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("capture #2 expected created-then-deleted dir absent from Core, got err=%v", err)
	}

	logicalChanges, err := view.Core.EnumerateChanges(true)
	if err != nil {
		t.Fatalf("EnumerateChanges #2 failed: %v", err)
	}
	if len(logicalChanges) != 0 {
		t.Fatalf("expected 0 logical changes after reverting all modifications and deleting ephemeral paths, got %+v", logicalChanges)
	}

	snap, err := view.Core.SnapshotJournal()
	if err != nil {
		t.Fatalf("SnapshotJournal failed: %v", err)
	}
	for _, ch := range snap.Changes {
		if ch.LogicalChange {
			t.Fatalf("expected no logical changes in journal snapshot after full revert, found %+v", ch)
		}
	}
	sumA := sha256.Sum256(seedA)
	if base, ok := snap.Bases["repo:seeded.txt"]; !ok || base.SHA256 != hex.EncodeToString(sumA[:]) {
		t.Fatalf("expected original immutable seed base retained in journal snapshot, got %+v", snap.Bases["repo:seeded.txt"])
	}
}

func TestEagerCopyRepeatCaptureSymlinksAndXattrErrorPropagation(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	srcRoot := filepath.Join(tmp, "src-sym")
	if err := os.MkdirAll(srcRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "a.txt"), []byte("A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "b.txt"), []byte("B"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(srcRoot, "seed-link")); err != nil {
		t.Fatal(err)
	}

	adp := NewEagerCopyAdapter(ReasonMountDenied)
	view, err := adp.Prepare(ctx, PrepareRequest{
		ViewID:     "repeat-symlink-1",
		RuntimeDir: filepath.Join(tmp, "rt"),
		Grants:     []RootGrant{{RootID: "repo", Label: "repo", SourcePath: srcRoot, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	defer func() { _, _ = adp.Release(ctx, view.ViewID) }()

	seedLinkPath := filepath.Join(view.PrimaryWorkPath, "seed-link")
	newLinkPath := filepath.Join(view.PrimaryWorkPath, "new-link")

	// Capture #1: retarget seed-link -> b.txt, create new-link -> b.txt.
	if err := os.Remove(seedLinkPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("b.txt", seedLinkPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("b.txt", newLinkPath); err != nil {
		t.Fatal(err)
	}
	_, rel1, err := adp.CheckpointBarrier(ctx, view.ViewID)
	if err != nil {
		t.Fatalf("CheckpointBarrier #1 failed: %v", err)
	}
	_ = rel1()

	// Capture #2: keep new-link unchanged (must not fail with ErrExist) and revert seed-link -> a.txt.
	if err := os.Remove(seedLinkPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", seedLinkPath); err != nil {
		t.Fatal(err)
	}
	_, rel2, err := adp.CheckpointBarrier(ctx, view.ViewID)
	if err != nil {
		t.Fatalf("CheckpointBarrier #2 failed: %v", err)
	}
	if target, err := view.Core.Readlink(layeredfs.QualifiedPath{RootID: "repo", Rel: "seed-link"}); err != nil || target != "a.txt" {
		t.Fatalf("expected seed-link reverted to a.txt on capture #2, err=%v target=%q", err, target)
	}
	if target, err := view.Core.Readlink(layeredfs.QualifiedPath{RootID: "repo", Rel: "new-link"}); err != nil || target != "b.txt" {
		t.Fatalf("expected unchanged new-link to remain b.txt on capture #2, err=%v target=%q", err, target)
	}
	_ = rel2()

	// Capture #3: delete new-link and verifyreadPathXattrs error propagation preserves existing xattrs in Core.
	if err := os.Remove(newLinkPath); err != nil {
		t.Fatal(err)
	}
	qpA := layeredfs.QualifiedPath{RootID: "repo", Rel: "a.txt"}
	if err := view.Core.SetXattr(qpA, "user.keep", []byte("preserved"), true); err != nil {
		t.Fatal(err)
	}
	readPathXattrsHook = func(p string) error {
		if strings.HasSuffix(p, "a.txt") {
			return syscall.EIO
		}
		return nil
	}
	defer func() { readPathXattrsHook = nil }()

	if _, _, err := adp.CheckpointBarrier(ctx, view.ViewID); !errors.Is(err, syscall.EIO) {
		t.Fatalf("expected CheckpointBarrier to propagate readPathXattrs EIO error, got %v", err)
	}
	gotAttr, err := view.Core.GetXattr(qpA, "user.keep", true)
	if err != nil || string(gotAttr) != "preserved" {
		t.Fatalf("expected failed readPathXattrs not to remove existing xattrs in Core, err=%v val=%q", err, string(gotAttr))
	}

	// Clear fault hook and verify recovery on next capture.
	readPathXattrsHook = nil
	_, rel3, err := adp.CheckpointBarrier(ctx, view.ViewID)
	if err != nil {
		t.Fatalf("CheckpointBarrier recovery after transient xattr error failed: %v", err)
	}
	if _, err := view.Core.Lstat(layeredfs.QualifiedPath{RootID: "repo", Rel: "new-link"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected deleted new-link to be unlinked in Core, got err=%v", err)
	}
	_ = rel3()
}
