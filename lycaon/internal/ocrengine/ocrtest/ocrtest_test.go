package ocrtest

import (
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/ocrengine"
)

// recordingTB records how SkipIfNoEngine ended a test. Like the real methods,
// Skip and Fatal stop the calling goroutine.
type recordingTB struct {
	testing.TB
	skipped, failed bool
}

func (r *recordingTB) Helper() {}
func (r *recordingTB) Skip(...any) {
	r.skipped = true
	runtime.Goexit()
}
func (r *recordingTB) Fatal(...any) {
	r.failed = true
	runtime.Goexit()
}

// gate runs SkipIfNoEngine on its own goroutine and reports how it ended.
func gate(t *testing.T) *recordingTB {
	tb := &recordingTB{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		SkipIfNoEngine(tb)
	}()
	<-done
	return tb
}

// withoutEngine leaves no verified engine in reach on any platform: macOS and
// other platforms pin none, and the Linux roots are empty.
func withoutEngine(t *testing.T) {
	t.Setenv(ocrengine.EnvRoot, t.TempDir())
	t.Setenv("LYCAON_ENGINE_ROOT", t.TempDir())
}

func TestSkipIfNoEngineSkipsWithoutAnEngine(t *testing.T) {
	withoutEngine(t)
	t.Setenv(ocrengine.EnvRequired, "")
	tb := gate(t)
	if !tb.skipped || tb.failed {
		t.Fatalf("skipped=%t failed=%t, want a skip", tb.skipped, tb.failed)
	}
}

func TestSkipIfNoEngineFailsWhenTheEngineIsRequired(t *testing.T) {
	withoutEngine(t)
	t.Setenv(ocrengine.EnvRequired, "1")
	tb := gate(t)
	if !tb.failed || tb.skipped {
		t.Fatalf("skipped=%t failed=%t, want a failure", tb.skipped, tb.failed)
	}
}
