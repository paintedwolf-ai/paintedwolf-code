package ocrtest

import (
	"testing"

	"github.com/lycaon/lycaon/internal/ocrengine"
)

// recordingTB records how SkipIfNoEngine ended a test instead of ending it.
type recordingTB struct {
	testing.TB
	skipped, failed bool
}

func (r *recordingTB) Helper()      {}
func (r *recordingTB) Skip(...any)  { r.skipped = true }
func (r *recordingTB) Fatal(...any) { r.failed = true }

// withoutEngine leaves no verified engine in reach on any platform: macOS and
// other platforms pin none, and the Linux roots are empty.
func withoutEngine(t *testing.T) {
	t.Setenv(ocrengine.EnvRoot, t.TempDir())
	t.Setenv("LYCAON_ENGINE_ROOT", t.TempDir())
}

func TestSkipIfNoEngineSkipsWithoutAnEngine(t *testing.T) {
	withoutEngine(t)
	t.Setenv(ocrengine.EnvRequired, "")
	tb := &recordingTB{TB: t}
	SkipIfNoEngine(tb)
	if !tb.skipped || tb.failed {
		t.Fatalf("skipped=%t failed=%t, want a skip", tb.skipped, tb.failed)
	}
}

func TestSkipIfNoEngineFailsWhenTheEngineIsRequired(t *testing.T) {
	withoutEngine(t)
	t.Setenv(ocrengine.EnvRequired, "1")
	tb := &recordingTB{TB: t}
	SkipIfNoEngine(tb)
	if !tb.failed || tb.skipped {
		t.Fatalf("skipped=%t failed=%t, want a failure", tb.skipped, tb.failed)
	}
}
