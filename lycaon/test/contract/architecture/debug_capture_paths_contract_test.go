package contract

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/debugpaths"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// The session tree the capture script creates is the tree the viewer reads:
// the script runs against a scratch configuration root, and the session
// directory and latest link it leaves are where debugpaths looks.
func TestDebugCaptureScriptTreeMatchesLayout(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv(configdir.EnvConfigDir, configDir)
	t.Setenv(debugpaths.SessionDirEnv, "")
	script := filepath.Join(contractcheck.RepoRoot(t), "scripts", "debug-session.sh")
	cmd := exec.CommandContext(t.Context(), "bash", script)
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	contractcheck.FailErr(t, "run debug-session.sh: "+string(out), err)

	if !strings.HasPrefix(debugpaths.DebugRoot(), configDir) {
		t.Fatalf("debug root %q is not under the configured root %q", debugpaths.DebugRoot(), configDir)
	}
	target, err := os.Readlink(debugpaths.LatestLink())
	contractcheck.FailErr(t, "read latest link", err)
	if filepath.Dir(target) != debugpaths.SessionsRoot() {
		t.Fatalf("latest link points at %q, want a directory in %q", target, debugpaths.SessionsRoot())
	}
	if _, err := debugpaths.ParseSessionStamp(filepath.Base(target)); err != nil {
		t.Fatalf("session directory %q is not a session stamp: %v", filepath.Base(target), err)
	}
	info, err := os.Stat(target)
	contractcheck.FailErr(t, "stat session directory", err)
	if !info.IsDir() {
		t.Fatalf("session path %q is not a directory", target)
	}
	entries, err := os.ReadDir(debugpaths.SessionsRoot())
	contractcheck.FailErr(t, "list sessions", err)
	if len(entries) != 1 || entries[0].Name() != filepath.Base(target) {
		t.Fatalf("sessions root holds %v, want only %s", entries, filepath.Base(target))
	}
}
