package projectsource

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSourceStreamingTransferSharesRecoveryBytes(t *testing.T) {
	service, p, rootPath, _ := sourceMutationFixture(t)
	root, err := os.OpenRoot(rootPath)
	testutil.FailErr(t, "open tree", err)
	defer func() { _ = root.Close() }()
	body := bytes.Repeat([]byte("streamed without cloning\n"), 10000)
	testutil.FailErr(t, "seed source", root.WriteFile("file", body, 0o600))
	in, err := root.Open("file")
	testutil.FailErr(t, "open source", err)
	defer func() { _ = in.Close() }()
	out, err := root.OpenFile("copy", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	testutil.FailErr(t, "open destination", err)
	defer func() { _ = out.Close() }()
	plan := &sourceMutationPlan{
		sourceMutationRecovery:    sourceMutationRecovery{RecoveryID: uuid.NewString()},
		sourceMutationAttribution: sourceMutationAttribution{ProjectID: p.ID},
		Kind:                      "copy",
	}
	capture, err := service.recovery.beginRecoveryCapture(t.Context(), plan, "copying")
	testutil.FailErr(t, "begin capture", err)
	defer capture.close()
	transfer := &sourceTreeTransfer{ctx: t.Context(), capture: capture, progress: capture.progress, digest: capture.digest}
	entry, err := transfer.transferFileBytes(root, "file", root, "copy", in, out, false, sourceledger.RecoveryEntry{Path: ".", Mode: 0o600})
	testutil.FailErr(t, "stream and retain", err)
	testutil.FailErr(t, "finish capture", capture.finish(t.Context()))
	_, err = out.Seek(0, io.SeekStart)
	testutil.FailErr(t, "rewind destination", err)
	copied, err := io.ReadAll(out)
	testutil.FailErr(t, "read destination", err)
	var retained bytes.Buffer
	testutil.FailErr(t, "read recovery", service.recovery.copyRecoveryFile(t.Context(), entry.SHA, &retained))
	if !bytes.Equal(copied, body) || !bytes.Equal(retained.Bytes(), body) {
		t.Fatal("streamed destination and recovery differ")
	}
}

func TestSourceFingerprintReportsAndCancelsWithinOneFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large")
	testutil.FailErr(t, "seed source", os.WriteFile(path, make([]byte, 1<<20), 0o600))
	info, err := os.Stat(path)
	testutil.FailErr(t, "stat source", err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var observed int64
	ctx = WithSourceProgress(ctx, func(p SourceProgress) {
		if p.Bytes > 0 {
			observed = p.Bytes
			cancel()
		}
	})
	progress := newSourceWorkProgress(ctx, "verifying")
	progress.last = time.Time{}
	_, err = fingerprintSourceFile(ctx, info, func() (*os.File, error) { return os.Open(path) }, progress)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("verification cancellation = %v", err)
	}
	if observed <= 0 || observed >= info.Size() {
		t.Fatalf("no incremental byte progress: %d", observed)
	}
}

func TestSourceFingerprintRejectsEditsDuringRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	testutil.FailErr(t, "seed source", os.WriteFile(path, make([]byte, 1<<20), 0o600))
	info, err := os.Stat(path)
	testutil.FailErr(t, "stat source", err)
	changed := false
	ctx := WithSourceProgress(t.Context(), func(p SourceProgress) {
		if p.Bytes == 0 || changed {
			return
		}
		changed = true
		testutil.FailErr(t, "change source version", os.Chtimes(path, info.ModTime(), info.ModTime().Add(time.Second)))
	})
	progress := newSourceWorkProgress(ctx, "verifying")
	progress.last = time.Time{}
	_, err = fingerprintSourceFile(ctx, info, func() (*os.File, error) { return os.Open(path) }, progress)
	if !errors.Is(err, ErrSourceMutationDiverged) {
		t.Fatalf("changed source accepted: %v", err)
	}
}
