package contract

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestReleaseStagingRejectsLocalOpenGrepCandidate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		script string
		args   []string
	}{
		{"stage-engine.sh", []string{"--release"}},
		{"stage-engine.sh", []string{"--minimal", "--release"}},
		{"den-build-bundle.sh", nil},
		{"den-build-app.sh", nil},
		{"release-stage-platform-artifacts.sh", nil},
	} {
		t.Run(tc.script+strings.Join(tc.args, "-"), func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			script := filepath.Join(root, "scripts", tc.script)
			contractcheck.FailErr(t, "create scratch scripts", os.MkdirAll(filepath.Dir(script), 0o700))
			contractcheck.FailErr(t, "copy staging script", os.WriteFile(script, []byte(contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "scripts/"+tc.script)), 0o700))
			payload := filepath.Join(root, "lycaon-den", "src-tauri", "engine-root", "sentinel")
			contractcheck.FailErr(t, "create existing engine root", os.MkdirAll(filepath.Dir(payload), 0o700))
			contractcheck.FailErr(t, "write existing payload", os.WriteFile(payload, []byte("preserve"), 0o600))
			cmd := exec.CommandContext(t.Context(), "bash", append([]string{script}, tc.args...)...)
			cmd.Env = append(os.Environ(), "LYCAON_OPENGREP_CANDIDATE="+filepath.Join(root, "candidate"))
			output, err := cmd.CombinedOutput()
			if err == nil || cmd.ProcessState.ExitCode() != 2 || !strings.Contains(string(output), "local Opengrep candidates cannot be used in release packaging") {
				t.Fatalf("release selection was not rejected: %v\n%s", err, output)
			}
			contents, err := os.ReadFile(payload)
			contractcheck.FailErr(t, "read preserved payload", err)
			if string(contents) != "preserve" {
				t.Fatal("release rejection changed the existing engine payload")
			}
			if _, err := os.Stat(filepath.Join(root, "lycaon-den", "src-tauri", "binaries")); !os.IsNotExist(err) {
				t.Fatalf("release rejection reached binary staging: %v", err)
			}
		})
	}
}
