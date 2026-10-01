package checkpoint

import (
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCheckpointCaptureFailedOpenPreservesCurrentInterval(t *testing.T) {
	t.Parallel()
	store, dir := newTestCheckpointStore(t)
	var capture Capture
	writeProjectFile(t, dir, "main.go", "before")
	testutil.FailErr(t, "open first anchor", capture.Open(t.Context(), store, "session", "first"))
	capture.RecordPath(t.Context(), store, "session", "main.go")
	writeProjectFile(t, dir, "main.go", "after")
	if err := capture.Open(t.Context(), store, "session", "../invalid"); err == nil {
		t.Fatal("invalid anchor was accepted")
	}
	capture.RecordPath(t.Context(), store, "session", "main.go")
	manifest, err := store.Load(t.Context(), "session", "first")
	testutil.FailErr(t, "load first anchor", err)
	blob, err := store.objects.GetSHA(manifest.Paths["main.go"].SHA256)
	testutil.FailErr(t, "read pre-image", err)
	if string(blob) != "before" {
		t.Fatalf("pre-image changed to %q", blob)
	}
}

func TestCheckpointCaptureSerializesFirstTouchesAndReset(t *testing.T) {
	t.Parallel()
	store, dir := newTestCheckpointStore(t)
	var capture Capture
	writeProjectFile(t, dir, "main.go", "before")
	testutil.FailErr(t, "open anchor", capture.Open(t.Context(), store, "session", "anchor"))
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() { capture.RecordPath(t.Context(), store, "session", "main.go") })
	}
	workers.Wait()
	capture.Reset("session")
	capture.RecordPath(t.Context(), store, "session", "later.go")
	manifest, err := store.Load(t.Context(), "session", "anchor")
	testutil.FailErr(t, "load anchor", err)
	if len(manifest.Paths) != 1 {
		t.Fatalf("capture escaped its interval: %+v", manifest.Paths)
	}
	if _, ok := manifest.Paths["main.go"]; !ok {
		t.Fatal("concurrent touches lost the file")
	}
}
