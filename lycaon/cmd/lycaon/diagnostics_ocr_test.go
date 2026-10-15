package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/ocrengine"
	"github.com/lycaon/lycaon/internal/ocrengine/ocrtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/visualscreen"
)

// The self-check reads its rendered fixture with the platform recognizer:
// Apple Vision on macOS, the verified bundled engine on Linux.
func TestDiagnosticsOCREngineReadsTheFixture(t *testing.T) {
	if runtime.GOOS == "linux" {
		ocrtest.SkipIfNoEngine(t)
	} else if runtime.GOOS != "darwin" {
		t.Skip("no text recognizer on this platform")
	}
	if err := runDiagnostics(t.Context(), []string{"ocr-engine"}); err != nil {
		testutil.FailErr(t, "pw diagnostics ocr-engine", err)
	}
}

// bundledOnly runs a case that exercises the bundled engine's resolution,
// with no staged or installed engine in reach.
func bundledOnly(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("only Linux resolves a bundled engine")
	}
	root := filepath.Join(t.TempDir(), "ocr")
	t.Setenv(ocrengine.EnvRoot, root)
	t.Setenv("LYCAON_ENGINE_ROOT", t.TempDir())
	return root
}

func TestDiagnosticsOCREngineReportsAMissingEngine(t *testing.T) {
	bundledOnly(t)
	err := runDiagnostics(t.Context(), []string{"ocr-engine"})
	var ue *ocrengine.UnavailableError
	if !errors.Is(err, visualscreen.ErrOCRUnavailable) || !errors.As(err, &ue) || ue.Reason != ocrengine.ReasonMissing {
		t.Fatalf("missing engine err = %v", err)
	}
}

// A staged engine whose payload differs from the pin is refused by name.
func TestDiagnosticsOCREngineRefusesAnAlteredPayload(t *testing.T) {
	root := bundledOnly(t)
	for _, rel := range []string{"bin/tesseract", "tessdata/eng.traineddata", "tessdata/osd.traineddata"} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			testutil.FailErr(t, "mkdir", err)
		}
		if err := os.WriteFile(path, []byte("altered"), 0o755); err != nil {
			testutil.FailErr(t, "write payload", err)
		}
	}
	err := runDiagnostics(t.Context(), []string{"ocr-engine"})
	var ue *ocrengine.UnavailableError
	if !errors.As(err, &ue) || ue.Reason != ocrengine.ReasonDigestMismatch || !strings.Contains(err.Error(), "differs from the pinned payload") {
		t.Fatalf("altered payload err = %v", err)
	}
}

func TestDiagnosticsUsageNamesTheOCRSelfCheck(t *testing.T) {
	err := runDiagnostics(t.Context(), []string{"unknown"})
	if err == nil || !strings.Contains(err.Error(), "| ocr-engine |") {
		t.Fatalf("usage err = %v", err)
	}
}
