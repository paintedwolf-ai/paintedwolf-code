package wiring

import (
	"os"
	"path/filepath"
	"testing"

	wire "github.com/lycaon/lycaon/pkg/api"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func WriteTestProjectApprovalsAllowWrite(t *testing.T, dir string) {
	t.Helper()
	// These smokes exercise workflow behavior, not confinement.
	t.Setenv("LYCAON_BYPASS_APPROVALS", "1")
	overlay := filepath.Join(dir, settingsoverlay.DirName())
	if err := os.MkdirAll(overlay, 0o700); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	body := "rules:\n  - category: tool\n    pattern: write\n    effect: allow\n  - category: tool\n    pattern: edit\n    effect: allow\n"
	if err := os.WriteFile(filepath.Join(overlay, "approvals.yaml"), []byte(body), 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}
}

func WriteImplementFixture(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	return path
}

func PtrKind(k wire.CheckpointKind) *wire.CheckpointKind {
	return &k
}

type FixedVerifyConfig string

func (config FixedVerifyConfig) VerifyTestCommand(string) string {
	return string(config)
}
