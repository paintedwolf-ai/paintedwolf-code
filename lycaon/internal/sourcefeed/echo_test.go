package sourcefeed

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHostEchoSuppressionAllowsImmediateOutsideChanges(t *testing.T) {
	for _, operation := range []string{"write", "delete", "same metadata", "chmod"} {
		t.Run(operation, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "source.txt")
			testutil.FailErr(t, "write host content", os.WriteFile(file, []byte("host"), 0o644))
			info, err := os.Stat(file)
			testutil.FailErr(t, "stat host content", err)
			NoteHostWrite(file)
			if !isRecentHostWrite(file) {
				t.Fatal("unchanged host content did not suppress its echo")
			}
			switch operation {
			case "delete":
				testutil.FailErr(t, "outside delete", os.Remove(file))
			case "chmod":
				testutil.FailErr(t, "outside chmod", os.Chmod(file, 0o600))
			default:
				testutil.FailErr(t, "outside write", os.WriteFile(file, []byte("user"), 0o644))
				if operation == "same metadata" {
					testutil.FailErr(t, "preserve timestamp", os.Chtimes(file, info.ModTime(), info.ModTime()))
				}
			}
			if isRecentHostWrite(file) {
				t.Fatal("outside change was suppressed by the recent host write")
			}
		})
	}
}

func TestPublishedDigestDoesNotAdoptAnOutsideWriteBeforeDelivery(t *testing.T) {
	file := filepath.Join(t.TempDir(), "source.txt")
	testutil.FailErr(t, "outside write already landed", os.WriteFile(file, []byte("outside"), 0o644))
	digest := sha256.Sum256([]byte("host"))
	noteHostWritePaths([]hostWriteTarget{{path: file, digest: hex.EncodeToString(digest[:])}})
	if isRecentHostWrite(file) {
		t.Fatal("published host digest adopted different live bytes")
	}
}

// D10 invariant test for CF05-DEF-01 (F04-EDGE-02, Decision 043):
// A host rename without foreground content hashing suppresses both its destination
// write echo and source delete echo using owner-captured stat + ctime proof, while
// still delivering any subsequent genuine external edit (including preserved mtime+size),
// atomic replace, chmod, delete, or source recreation.
func TestHostRenameSuppressesEchoWithoutBlockingSubsequentExternalEdits(t *testing.T) {
	for _, followUp := range []string{"outside_write", "same_size_and_mtime", "atomic_replace", "chmod", "delete", "recreate_source"} {
		t.Run(followUp, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "seed.txt")
			dst := filepath.Join(dir, "renamed.txt")
			testutil.FailErr(t, "seed", os.WriteFile(src, []byte("seed content\n"), 0o644))
			testutil.FailErr(t, "rename", os.Rename(src, dst))
			infoBefore, err := os.Stat(dst)
			testutil.FailErr(t, "stat renamed", err)

			receipt, ok := CaptureMetadataReceipt(dst)
			if !ok || receipt == nil {
				t.Fatal("CaptureMetadataReceipt did not capture rename metadata")
			}
			absent := hostWriteState{absent: true}
			noteHostWritePaths([]hostWriteTarget{
				{path: dst, preCaptured: &receipt.state},
				{path: src, preCaptured: &absent},
			})

			if !isRecentHostWrite(dst) {
				t.Fatal("host rename destination echo was not suppressed")
			}
			if !isRecentHostWrite(src) {
				t.Fatal("host rename source delete echo was not suppressed")
			}

			switch followUp {
			case "outside_write":
				testutil.FailErr(t, "outside write", os.WriteFile(dst, []byte("external edit after rename\n"), 0o644))
				if isRecentHostWrite(dst) {
					t.Fatal("subsequent external write to renamed file was falsely suppressed")
				}
			case "same_size_and_mtime":
				// Same byte length as "seed content\n" (13 bytes) and restored mtime; ctime still advances.
				testutil.FailErr(t, "same-size write", os.WriteFile(dst, []byte("USER CONTENT\n"), 0o644))
				testutil.FailErr(t, "preserve mtime", os.Chtimes(dst, infoBefore.ModTime(), infoBefore.ModTime()))
				if isRecentHostWrite(dst) {
					t.Fatal("subsequent external write with preserved size and mtime was falsely suppressed")
				}
			case "atomic_replace":
				tmp := filepath.Join(dir, ".tmp.renamed.txt")
				testutil.FailErr(t, "write tmp", os.WriteFile(tmp, []byte("seed content\n"), 0o644))
				testutil.FailErr(t, "replace", os.Rename(tmp, dst))
				if isRecentHostWrite(dst) {
					t.Fatal("subsequent external atomic replace (new inode) was falsely suppressed")
				}
			case "chmod":
				testutil.FailErr(t, "chmod", os.Chmod(dst, 0o600))
				if isRecentHostWrite(dst) {
					t.Fatal("subsequent external chmod on renamed file was falsely suppressed")
				}
			case "delete":
				testutil.FailErr(t, "delete", os.Remove(dst))
				if isRecentHostWrite(dst) {
					t.Fatal("subsequent external delete of renamed file was falsely suppressed")
				}
			case "recreate_source":
				testutil.FailErr(t, "recreate", os.WriteFile(src, []byte("new file at old path\n"), 0o644))
				if isRecentHostWrite(src) {
					t.Fatal("subsequent external creation at old rename source path was falsely suppressed")
				}
			}
		})
	}
}

// Decision 043 barrier test: a concurrent external write to dst or recreation of src
// occurring AFTER the rename owner captured its receipt and BEFORE DeliverCommitted()
// publishes echo suppression must NEVER be adopted as the host mutation's state.
func TestHostRenameReceiptBarrierDoesNotAdoptConcurrentOutsideChange(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "seed.txt")
	dst := filepath.Join(dir, "renamed.txt")
	testutil.FailErr(t, "seed", os.WriteFile(src, []byte("seed content\n"), 0o644))
	testutil.FailErr(t, "rename", os.Rename(src, dst))

	receipt, ok := CaptureMetadataReceipt(dst)
	if !ok || receipt == nil {
		t.Fatal("CaptureMetadataReceipt failed")
	}

	// Stage delivery with the committed rename owner's receipt, pausing before DeliverCommitted().
	prepared, err := prepareChanges([]Change{{
		ProjectID: "proj-barrier", WorkspaceID: "ws-barrier", WorkspaceKind: "project",
		RootID: "r1", Path: "renamed.txt", FromPath: "seed.txt",
		AbsPath: dst, FromAbsPath: src, MetadataReceipt: receipt,
		Op: "rename", Origin: "user",
	}}, false)
	testutil.FailErr(t, "prepareChanges", err)

	// Barrier: concurrent outside writer mutates dst AND recreates src BEFORE receipt publication!
	testutil.FailErr(t, "concurrent outside dst write", os.WriteFile(dst, []byte("concurrent outside edit\n"), 0o644))
	testutil.FailErr(t, "concurrent outside src recreate", os.WriteFile(src, []byte("recreated outside\n"), 0o644))

	// Now publish the staged receipt (simulating DeliverCommitted after the barrier).
	noteHostWritePaths(prepared.writePaths)

	if isRecentHostWrite(dst) {
		t.Fatal("receipt publication adopted concurrent outside write on destination path")
	}
	if isRecentHostWrite(src) {
		t.Fatal("receipt publication adopted concurrent outside recreation on source path")
	}

	// Also verify fail-open when MetadataReceipt is nil (no owner proof).
	noProof, err := prepareChanges([]Change{{
		ProjectID: "proj-barrier", WorkspaceID: "ws-barrier", WorkspaceKind: "project",
		RootID: "r1", Path: "renamed.txt", FromPath: "seed.txt",
		AbsPath: dst, FromAbsPath: src,
		Op: "rename", Origin: "user",
	}}, false)
	testutil.FailErr(t, "prepareChanges nil receipt", err)
	for _, wp := range noProof.writePaths {
		if wp.path == dst {
			t.Fatal("rename without owner MetadataReceipt must fail open for destination")
		}
	}
}

// Decision 056 pre-capture window controls:
// 1. Simple matching rename binds post-rename receipt to pre-rename owner metadata and suppresses echo.
// 2. Replacement between rename and capture fails open (nil receipt -> external change observed).
// 3. Same-inode outside write between rename and capture fails open (nil receipt -> external change observed).
func TestHostRenamePreCaptureWindowControls(t *testing.T) {
	t.Run("simple_matching_rename", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "seed.txt")
		dst := filepath.Join(dir, "renamed.txt")
		testutil.FailErr(t, "seed", os.WriteFile(src, []byte("seed content\n"), 0o644))
		before, ok := CaptureMetadataReceipt(src)
		if !ok || before == nil {
			t.Fatal("pre-rename CaptureMetadataReceipt failed")
		}
		testutil.FailErr(t, "rename", os.Rename(src, dst))
		after, ok := CaptureVerifiedRenameReceipt(dst, before)
		if !ok || after == nil {
			t.Fatal("CaptureVerifiedRenameReceipt rejected matching rename")
		}
		prepared, err := prepareChanges([]Change{{
			ProjectID: "proj-prec", WorkspaceID: "ws-prec", WorkspaceKind: "project",
			RootID: "r1", Path: "renamed.txt", FromPath: "seed.txt",
			AbsPath: dst, FromAbsPath: src, MetadataReceipt: after,
			Op: "rename", Origin: "user",
		}}, false)
		testutil.FailErr(t, "prepareChanges", err)
		noteHostWritePaths(prepared.writePaths)
		if !isRecentHostWrite(dst) || !isRecentHostWrite(src) {
			t.Fatal("matching rename did not suppress both destination and source echoes")
		}
	})

	t.Run("replacement_before_capture_fails_open", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "seed.txt")
		dst := filepath.Join(dir, "renamed.txt")
		testutil.FailErr(t, "seed", os.WriteFile(src, []byte("seed content\n"), 0o644))
		before, ok := CaptureMetadataReceipt(src)
		if !ok || before == nil {
			t.Fatal("pre-rename CaptureMetadataReceipt failed")
		}
		testutil.FailErr(t, "rename", os.Rename(src, dst))
		// External replacement happens BEFORE post-rename receipt capture.
		tmp := filepath.Join(dir, ".tmp.replace")
		testutil.FailErr(t, "write replacement", os.WriteFile(tmp, []byte("seed content\n"), 0o644))
		testutil.FailErr(t, "replace dst", os.Rename(tmp, dst))
		after, ok := CaptureVerifiedRenameReceipt(dst, before)
		if ok || after != nil {
			t.Fatal("CaptureVerifiedRenameReceipt adopted a replaced inode before capture")
		}
	})

	t.Run("same_inode_outside_write_before_capture_fails_open", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "seed.txt")
		dst := filepath.Join(dir, "renamed.txt")
		testutil.FailErr(t, "seed", os.WriteFile(src, []byte("seed content\n"), 0o644))
		before, ok := CaptureMetadataReceipt(src)
		if !ok || before == nil {
			t.Fatal("pre-rename CaptureMetadataReceipt failed")
		}
		testutil.FailErr(t, "rename", os.Rename(src, dst))
		// Same-inode external write happens BEFORE post-rename receipt capture.
		testutil.FailErr(t, "same-inode outside write", os.WriteFile(dst, []byte("outside mutated before capture\n"), 0o644))
		after, ok := CaptureVerifiedRenameReceipt(dst, before)
		if ok || after != nil {
			t.Fatal("CaptureVerifiedRenameReceipt adopted a same-inode outside write before capture")
		}
	})
}
