package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCombinesTranscriptFiles(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.json")
	second := filepath.Join(dir, "second.json")
	if err := os.WriteFile(first, []byte("[]"), 0o600); err != nil {
		t.Fatalf("write first transcript: %v", err)
	}
	if err := os.WriteFile(second, []byte("[]"), 0o600); err != nil {
		t.Fatalf("write second transcript: %v", err)
	}

	var output bytes.Buffer
	if err := run([]string{first, second}, &output); err != nil {
		t.Fatalf("run report: %v", err)
	}
	for _, want := range []string{`"offered": 0`, `"followed": 0`, `"offers_by_code": {}`} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("report output does not contain %q:\n%s", want, output.String())
		}
	}
}

func TestRunReportsUnreadableAndInvalidTranscripts(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{filepath.Join(t.TempDir(), "missing.json")}, &output); err == nil {
		t.Fatal("missing transcript succeeded")
	}

	invalid := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(invalid, []byte("{"), 0o600); err != nil {
		t.Fatalf("write invalid transcript: %v", err)
	}
	if err := run([]string{invalid}, &output); err == nil || !strings.Contains(err.Error(), invalid) {
		t.Fatalf("invalid transcript error = %v", err)
	}
}
