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

// D10 invariant test for CF05-DEF-01 (F04-EDGE-02):
// A host rename without foreground content hashing suppresses both its destination
// write echo and source delete echo, while still delivering any subsequent genuine
// external edit, atomic replace, chmod, delete, or source recreation.
func TestHostRenameSuppressesEchoWithoutBlockingSubsequentExternalEdits(t *testing.T) {
	for _, followUp := range []string{"outside_write", "atomic_replace", "chmod", "delete", "recreate_source"} {
		t.Run(followUp, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "seed.txt")
			dst := filepath.Join(dir, "renamed.txt")
			testutil.FailErr(t, "seed", os.WriteFile(src, []byte("seed content\n"), 0o644))
			testutil.FailErr(t, "rename", os.Rename(src, dst))

			noteHostWritePaths([]hostWriteTarget{
				{path: dst, metadataOnly: true},
				{path: src, metadataOnly: true},
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
