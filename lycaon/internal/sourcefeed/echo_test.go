package sourcefeed

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sourceblob"
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

// D10 invariant test for CF05-DEF-01 (F04-EDGE-02, Decisions 043/056/059):
// A host rename backed by the committed ledger's known AfterSHA256 + pre-rename AfterMode
// (zero foreground content hashing) suppresses its own destination write echo and source
// delete echo, while still delivering every genuine external edit — including the combined
// adversarial same-inode, different-bytes, same-size, preserved-mtime edit!
func TestHostRenameSuppressesEchoWithoutBlockingSubsequentExternalEdits(t *testing.T) {
	seedBytes := []byte("seed content\n")
	seedSum := sha256.Sum256(seedBytes)
	seedSHA := hex.EncodeToString(seedSum[:])

	for _, followUp := range []string{"outside_write", "same_size_and_mtime", "atomic_replace", "chmod", "delete", "recreate_source"} {
		t.Run(followUp, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "seed.txt")
			dst := filepath.Join(dir, "renamed.txt")
			testutil.FailErr(t, "seed", os.WriteFile(src, seedBytes, 0o644))
			infoBefore, err := os.Lstat(src)
			testutil.FailErr(t, "stat before rename", err)
			testutil.FailErr(t, "rename", os.Rename(src, dst))

			prepared, err := prepareChanges([]Change{{
				ProjectID: "proj-f04", WorkspaceID: "ws-f04", WorkspaceKind: "project",
				RootID: "r1", Path: "renamed.txt", FromPath: "seed.txt",
				AbsPath: dst, FromAbsPath: src,
				AfterSHA256: seedSHA, AfterMode: infoBefore.Mode(),
				Op: "rename", Origin: "user",
			}}, false)
			testutil.FailErr(t, "prepareChanges", err)
			noteHostWritePaths(prepared.writePaths)

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
				testutil.FailErr(t, "same-size write", os.WriteFile(dst, []byte("USER CONTENT\n"), 0o644))
				testutil.FailErr(t, "preserve mtime", os.Chtimes(dst, infoBefore.ModTime(), infoBefore.ModTime()))
				if isRecentHostWrite(dst) {
					t.Fatal("subsequent external write with preserved size and mtime was falsely suppressed")
				}
			case "atomic_replace":
				tmp := filepath.Join(dir, ".tmp.renamed.txt")
				testutil.FailErr(t, "write tmp", os.WriteFile(tmp, []byte("USER CONTENT\n"), 0o644))
				testutil.FailErr(t, "replace", os.Rename(tmp, dst))
				if isRecentHostWrite(dst) {
					t.Fatal("subsequent external atomic replace was falsely suppressed")
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

// Decision 043 / 056 / 059 combined adversarial pre-capture & barrier control:
// Even if an external writer performs a same-inode in-place edit with DIFFERENT bytes,
// SAME size, and PRESERVED mtime between os.Rename() and DeliverCommitted() (the exact
// pre-capture window that fooled stat-only receipts), binding the suppression receipt to
// the committed ledger's pre-rename AfterSHA256 + AfterMode never adopts the outside edit
// and guarantees it reaches reconciliation.
func TestHostRenameAdversarialPreCaptureAndBarrierControls(t *testing.T) {
	seedBytes := []byte("seed content\n")
	seedSum := sha256.Sum256(seedBytes)
	seedSHA := hex.EncodeToString(seedSum[:])

	t.Run("adversarial_same_inode_same_size_preserved_mtime_before_publish", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "seed.txt")
		dst := filepath.Join(dir, "renamed.txt")
		testutil.FailErr(t, "seed", os.WriteFile(src, seedBytes, 0o644))
		infoBefore, err := os.Lstat(src)
		testutil.FailErr(t, "stat before rename", err)
		testutil.FailErr(t, "rename", os.Rename(src, dst))

		// Adversarial in-place write in the pre-capture window BEFORE commit/publish:
		// same inode, same size (13 bytes), same mode (0o644), preserved exact mtime!
		f, err := os.OpenFile(dst, os.O_WRONLY|os.O_TRUNC, 0o644)
		testutil.FailErr(t, "open dst in place", err)
		_, err = f.Write([]byte("EVIL CONTENT\n"))
		testutil.FailErr(t, "write adversarial bytes", err)
		testutil.FailErr(t, "close dst", f.Close())
		testutil.FailErr(t, "restore mtime", os.Chtimes(dst, infoBefore.ModTime(), infoBefore.ModTime()))
		testutil.FailErr(t, "concurrent outside src recreate", os.WriteFile(src, []byte("recreated\n"), 0o644))

		// Now commit/publish stages the ledger's pre-rename AfterSHA256 + pre-rename AfterMode.
		prepared, err := prepareChanges([]Change{{
			ProjectID: "proj-adv", WorkspaceID: "ws-adv", WorkspaceKind: "project",
			RootID: "r1", Path: "renamed.txt", FromPath: "seed.txt",
			AbsPath: dst, FromAbsPath: src,
			AfterSHA256: seedSHA, AfterMode: infoBefore.Mode(),
			Op: "rename", Origin: "user",
		}}, false)
		testutil.FailErr(t, "prepareChanges", err)
		noteHostWritePaths(prepared.writePaths)

		if isRecentHostWrite(dst) {
			t.Fatal("adversarial same-inode/same-size/preserved-mtime edit before publish was falsely suppressed")
		}
		if isRecentHostWrite(src) {
			t.Fatal("concurrent outside recreation of source path before publish was falsely suppressed")
		}
	})

	t.Run("git_oid_preexisting_rename_echo_and_adversarial_edit", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "seed.txt")
		dst := filepath.Join(dir, "renamed.txt")
		testutil.FailErr(t, "seed", os.WriteFile(src, seedBytes, 0o644))
		infoBefore, err := os.Lstat(src)
		testutil.FailErr(t, "stat before rename", err)
		testutil.FailErr(t, "rename", os.Rename(src, dst))

		gitHash := sourceblob.GitBlobSHA1(int64(len(seedBytes)))
		_, _ = gitHash.Write(seedBytes)
		seedGitOID := hex.EncodeToString(gitHash.Sum(nil))

		prepared, err := prepareChanges([]Change{{
			ProjectID: "proj-git", WorkspaceID: "ws-git", WorkspaceKind: "project",
			RootID: "r1", Path: "renamed.txt", FromPath: "seed.txt",
			AbsPath: dst, FromAbsPath: src,
			EchoGitOID: seedGitOID, AfterMode: infoBefore.Mode(), HasAfterMode: true,
			Op: "rename", Origin: "user",
		}}, false)
		testutil.FailErr(t, "prepareChanges git oid", err)
		if prepared.event.Changes[0].AfterSHA256 != "" {
			t.Fatalf("wire after_sha256 must stay empty, got %q", prepared.event.Changes[0].AfterSHA256)
		}
		noteHostWritePaths(prepared.writePaths)
		if !isRecentHostWrite(dst) {
			t.Fatal("git-oid rename destination echo was not suppressed")
		}
		// Adversarial same-inode, same-size, preserved-mtime outside edit must break suppression.
		f, err := os.OpenFile(dst, os.O_WRONLY|os.O_TRUNC, 0o644)
		testutil.FailErr(t, "open dst", err)
		_, err = f.Write([]byte("EVIL CONTENT\n"))
		testutil.FailErr(t, "write evil", err)
		testutil.FailErr(t, "close dst", f.Close())
		testutil.FailErr(t, "restore mtime", os.Chtimes(dst, infoBefore.ModTime(), infoBefore.ModTime()))
		if isRecentHostWrite(dst) {
			t.Fatal("adversarial edit on git-oid renamed file was falsely suppressed")
		}
	})

	t.Run("missing_ledger_digest_fails_open", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "seed.txt")
		dst := filepath.Join(dir, "renamed.txt")
		testutil.FailErr(t, "seed", os.WriteFile(src, seedBytes, 0o644))
		testutil.FailErr(t, "rename", os.Rename(src, dst))
		prepared, err := prepareChanges([]Change{{
			ProjectID: "proj-open", WorkspaceID: "ws-open", WorkspaceKind: "project",
			RootID: "r1", Path: "renamed.txt", FromPath: "seed.txt",
			AbsPath: dst, FromAbsPath: src,
			Op: "rename", Origin: "user",
		}}, false)
		testutil.FailErr(t, "prepareChanges nil digest", err)
		for _, wp := range prepared.writePaths {
			if wp.path == dst {
				t.Fatal("rename without committed ledger AfterSHA256 must fail open for destination")
			}
		}
	})
}
