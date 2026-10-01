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

func TestPrivateStagingPathStaysSuppressedAfterRemoval(t *testing.T) {
	file := filepath.Join(t.TempDir(), "staging")
	testutil.FailErr(t, "create staging", os.WriteFile(file, nil, 0o600))
	NoteHostTemporaryPath(file)
	testutil.FailErr(t, "finish staging", os.Remove(file))
	if !isRecentHostWrite(file) {
		t.Fatal("private staging removal leaked into source changes")
	}
}
