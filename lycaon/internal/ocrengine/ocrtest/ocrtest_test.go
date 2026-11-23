package ocrtest

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/ocrengine"
	"github.com/lycaon/lycaon/internal/testutil"
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

func TestConfigureStagedSelectsCheckoutRoot(t *testing.T) {
	checkout := t.TempDir()
	t.Chdir(checkout)
	t.Setenv(ocrengine.EnvRoot, "")
	testutil.FailErr(t, "write task file", os.WriteFile(filepath.Join(checkout, "Taskfile.yml"), nil, 0o600))
	root := filepath.Join(checkout, "lycaon-den", "src-tauri", "engine-root", ocrengine.DirName)
	testutil.FailErr(t, "create staged directory", os.MkdirAll(root, 0o755))
	configureStaged(t)
	if got := os.Getenv(ocrengine.EnvRoot); got != root {
		t.Fatalf("OCR root = %q, want %q", got, root)
	}

	override := t.TempDir()
	t.Setenv(ocrengine.EnvRoot, override)
	configureStaged(t)
	if got := os.Getenv(ocrengine.EnvRoot); got != override {
		t.Fatalf("configured OCR root = %q, want %q", got, override)
	}
}
