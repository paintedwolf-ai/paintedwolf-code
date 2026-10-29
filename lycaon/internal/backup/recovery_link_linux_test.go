//go:build linux

package backup

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fileclone"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A recovery point shares the immutable body and keeps its bytes after the
// live content-addressed body is collected.
func TestRecoveryLinkedBodySurvivesLiveCollection(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "live", "ab", "cdef.zst")
	recovery := filepath.Join(root, "recovery")
	payload := bytes.Repeat([]byte("retained history\n"), 1<<14)
	testutil.FailErr(t, "create live store", os.MkdirAll(filepath.Dir(live), 0o700))
	testutil.FailErr(t, "publish immutable body", os.WriteFile(live, payload, 0o400))
	source := archiveSource{path: live, kind: fileKindRegular, mode: 0o400, shareBody: true}
	sum, size, err := sourceDigest(t.Context(), source)
	testutil.FailErr(t, "digest live body", err)
	cloneable, err := fileclone.SupportedAt(root)
	testutil.FailErr(t, "probe clone support", err)

	shared, err := copySnapshotRegular(t.Context(), source, filepath.Join(recovery, "ab", "cdef.zst"))
	testutil.FailErr(t, "capture immutable body", err)
	if !shared {
		t.Fatal("immutable body was copied instead of shared")
	}
	liveInfo, err := os.Stat(live)
	testutil.FailErr(t, "inspect live inode", err)
	captured, err := os.Stat(filepath.Join(recovery, "ab", "cdef.zst"))
	testutil.FailErr(t, "inspect captured inode", err)
	t.Logf("capture on %s: clone=%t linked=%t payload=%d bytes", root, cloneable, os.SameFile(liveInfo, captured), len(payload))
	if !cloneable && !os.SameFile(liveInfo, captured) {
		t.Fatal("filesystem without reflink did not share the body inode")
	}

	testutil.FailErr(t, "collect live body", os.Remove(live))
	testutil.FailErr(t, "remove live store", os.RemoveAll(filepath.Join(root, "live")))
	testutil.FailErr(t, "verify retained body after collection", verifySnapshotFile(t.Context(), recovery, FileEntry{RelPath: "ab/cdef.zst", Kind: fileKindRegular, Size: size, SHA256: sum}))
	got, err := os.ReadFile(filepath.Join(recovery, "ab", "cdef.zst"))
	testutil.FailErr(t, "read retained body", err)
	if !bytes.Equal(got, payload) {
		t.Fatal("retained body changed after the live store was collected")
	}
	if captured.Mode().Perm() != 0o400 {
		t.Fatalf("retained body mode = %o, want read-only", captured.Mode().Perm())
	}
}
