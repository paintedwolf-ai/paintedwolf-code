package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunGeneratesAndChecksWorkerBranchTools(t *testing.T) {
	outGo := filepath.Join(t.TempDir(), "worker_branch_tools.generated.go")
	args := []string{"--out-go", outGo}
	var output bytes.Buffer
	if err := run(args, &output); err != nil {
		t.Fatalf("run codegen: %v", err)
	}

	generated, err := os.ReadFile(outGo)
	if err != nil {
		t.Fatalf("read generated worker branch tools: %v", err)
	}
	if !strings.Contains(string(generated), `"code_rewrite"`) {
		t.Fatalf("generated worker branch tools omit code_rewrite:\n%s", generated)
	}
	if err := run(append(args, "--check"), &output); err != nil {
		t.Fatalf("check generated tools: %v", err)
	}
}

func TestRunRequiresOutputPath(t *testing.T) {
	if err := run(nil, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "--out-go is required") {
		t.Fatalf("missing output path error = %v", err)
	}
}

func TestRunRejectsInvalidFlagsAndStaleOutput(t *testing.T) {
	if err := run([]string{"--unknown"}, &bytes.Buffer{}); err == nil {
		t.Fatal("unknown flag succeeded")
	}

	outGo := filepath.Join(t.TempDir(), "worker_branch_tools.generated.go")
	if err := os.WriteFile(outGo, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale output: %v", err)
	}
	err := run([]string{"--out-go", outGo, "--check"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale output error = %v", err)
	}
}
