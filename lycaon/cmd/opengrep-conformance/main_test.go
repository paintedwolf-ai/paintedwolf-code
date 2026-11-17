package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
)

func TestCLIRejectsZeroProjectTimeoutAndMissingScanner(t *testing.T) {
	var stderr bytes.Buffer
	if code := cli([]string{"-projects", "cases.yaml", "-project-timeout", "0s"}, &stderr); code != 1 || !strings.Contains(stderr.String(), "timeout") {
		t.Fatalf("zero project timeout exit=%d stderr=%q, want exit 1 with timeout error", code, stderr.String())
	}

	stderr.Reset()
	if code := cli([]string{"-admit", "intraprocedural"}, &stderr); code != 1 {
		t.Fatalf("-admit without -projects exit=%d, want 1", code)
	}

	t.Chdir(configlayout.FindModuleRoot())
	stderr.Reset()
	missingScanner := filepath.Join(t.TempDir(), "missing-opengrep")
	if code := cli([]string{"-opengrep", missingScanner, "-language", "python"}, &stderr); code != 1 {
		t.Fatalf("missing scanner exit=%d stderr=%q, want 1", code, stderr.String())
	}
}
