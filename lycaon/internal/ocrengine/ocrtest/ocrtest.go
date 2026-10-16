// Package ocrtest gates tests that run the bundled text recognizer.
package ocrtest

import (
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/ocrengine"
)

const noEngineMessage = "no verified bundled text recognizer: stage one with scripts/ocrengine-fetch.sh, " +
	"or point LYCAON_OCR_ROOT at a staged ocr directory"

// SkipIfNoEngine skips a test on a machine without a verified engine, or fails
// it when LYCAON_OCR_REQUIRED=1 says a skip would hide missing coverage.
func SkipIfNoEngine(t testing.TB) {
	t.Helper()
	ocrengine.TestingEnableBundledRoot()
	if _, err := ocrengine.Resolve(); err == nil {
		return
	}
	if os.Getenv(ocrengine.EnvRequired) == "1" {
		t.Fatal(noEngineMessage)
	}
	t.Skip(noEngineMessage)
}
