package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/egressclass"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// staticUpdaterEndpoint is the stable pointer the shipped client reads: the egress
// inventory's template applied to the registry's embedded key generation.
func staticUpdaterEndpoint(t *testing.T, root string) string {
	t.Helper()
	registry := contractcheck.ReadRepoFile(t, root, "packaging/update-keys.json")
	var parsed struct {
		EmbeddedGeneration int `json:"embedded_generation"`
	}
	contractcheck.FailErr(t, "parse update key registry", json.Unmarshal([]byte(registry), &parsed))
	if parsed.EmbeddedGeneration < 1 {
		t.Fatalf("update key registry embeds generation %d", parsed.EmbeddedGeneration)
	}
	endpoint := strings.ReplaceAll(egressclass.UpdateManifestEndpointTemplate, "{channel}", "stable")
	return strings.ReplaceAll(endpoint, "{key_generation}", fmt.Sprint(parsed.EmbeddedGeneration))
}

func TestReleaseHaltCLI(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	script := filepath.Join(root, "scripts/release-halt.sh")
	fixtures := filepath.Join(root, "lycaon/testdata/release-halt")
	current := filepath.Join(fixtures, "current-bad.json")
	lastGood := filepath.Join(fixtures, "last-good.json")

	run := func(t *testing.T, args ...string) (string, int) {
		t.Helper()
		cmd := exec.Command("bash", append([]string{script}, args...)...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "DOWNLOAD_BASE_URL=https://downloads.paintedwolf.dev")
		out, err := cmd.CombinedOutput()
		ec := 0
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				ec = ee.ExitCode()
			} else {
				t.Fatalf("run %v: %v\n%s", args, err, out)
			}
		}
		return string(out), ec
	}

	t.Run("file_dry_run", func(t *testing.T) {
		out, ec := run(t, "--channel", "stable", "--bad", "0.2.0", "--last-good", "0.1.0",
			"--current-file", current, "--release-file", lastGood, "--dry-run")
		if ec != 0 {
			t.Fatalf("exit %d: %s", ec, out)
		}
		if !strings.Contains(out, "dry-run ok") {
			t.Fatalf("expected dry-run ok: %s", out)
		}
	})

	t.Run("active_version_guard", func(t *testing.T) {
		out, ec := run(t, "--channel", "stable", "--bad", "0.3.0", "--last-good", "0.1.0",
			"--current-file", current, "--release-file", lastGood, "--dry-run")
		if ec != 1 || !strings.Contains(out, "active updater version is 0.2.0") {
			t.Fatalf("want active-version guard: ec=%d out=%s", ec, out)
		}
	})

	t.Run("usage", func(t *testing.T) {
		_, ec := run(t)
		if ec != 2 {
			t.Fatalf("want usage exit 2 got %d", ec)
		}
	})

	t.Run("self_test", func(t *testing.T) {
		out, ec := run(t, "--self-test")
		if ec != 0 {
			t.Fatalf("self-test failed: %d\n%s", ec, out)
		}
	})
}

func TestReleaseHaltWorkflowIsProtected(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	body := contractcheck.ReadRepoFile(t, root, ".github/workflows/release-halt.yml")
	for _, needle := range []string{
		"workflow_dispatch:",
		"environment: release-publication",
		"concurrency:",
		"group: release-static-update",
		"dry_run:",
		"scripts/release_withdrawal.py prepare",
		"scripts/release_withdrawal.py apply",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("release halt workflow missing %q", needle)
		}
	}
}
