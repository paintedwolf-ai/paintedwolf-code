// Package ocrtest gates tests that run the bundled text recognizer.
package ocrtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/ocrengine"
	"github.com/lycaon/lycaon/internal/testsetup/enginebundle"
)

const noEngineMessage = "no verified bundled text recognizer: stage one with ./task den:stage-engine -- --ocr-only, " +
	"or point LYCAON_OCR_ROOT at a staged ocr directory"

// SkipIfNoEngine skips a test on a machine without a verified engine, or fails
// it when LYCAON_OCR_REQUIRED=1 says a skip would hide missing coverage.
func SkipIfNoEngine(t testing.TB) {
	t.Helper()
	configureStaged(t)
	if _, err := ocrengine.Resolve(); err == nil {
		return
	}
	if os.Getenv(ocrengine.EnvRequired) == "1" {
		t.Fatal(noEngineMessage)
	}
	t.Skip(noEngineMessage)
}

// configureStaged selects the checkout's staged engine for this test.
func configureStaged(t testing.TB) {
	t.Helper()
	root := enginebundle.Root()
	if root == "" || strings.TrimSpace(os.Getenv(ocrengine.EnvRoot)) != "" {
		return
	}
	path := filepath.Join(root, ocrengine.DirName)
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		t.Setenv(ocrengine.EnvRoot, path)
	}
}
