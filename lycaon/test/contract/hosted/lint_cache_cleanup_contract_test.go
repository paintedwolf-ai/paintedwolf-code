package contract

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func TestLintCacheCleanupPreservesPublicationAndReportsDeletionFailures(t *testing.T) {
	t.Parallel()
	var action struct {
		Runs struct{ Steps []struct{ Name, Run string } }
	}
	data := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), ".github/actions/cache-lint-diagnostics/action.yml")
	contractcheck.FailErr(t, "decode lint cache action", yaml.Unmarshal([]byte(data), &action))
	var cleanup string
	for _, step := range action.Runs.Steps {
		if step.Name == "Delete superseded lint diagnostics" {
			cleanup = step.Run
		}
	}
	if cleanup == "" {
		t.Fatal("lint cache cleanup is missing")
	}
	for _, fixture := range []struct {
		name, keys, deleted string
		failDelete, wantErr bool
	}{
		{name: "current only", keys: "current\n"},
		{name: "empty"},
		{name: "save absent", keys: "old\n"},
		{name: "superseded", keys: "old\ncurrent\nolder\n", deleted: "old\nolder\n"},
		{name: "delete failure", keys: "current\nold\n", deleted: "old\n", failDelete: true, wantErr: true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			dir := t.TempDir()
			calls := filepath.Join(dir, "deleted")
			stub := `#!/bin/bash
set -eu
case "$2" in
  list) printf '%s' "$FIXTURE_KEYS" ;;
  delete) printf '%s\n' "$5" >> "$FIXTURE_CALLS"; test "$FIXTURE_FAIL" = false ;;
  *) exit 20 ;;
esac
`
			contractcheck.FailErr(t, "write gh fixture", os.WriteFile(filepath.Join(dir, "gh"), []byte(stub), 0o755))
			cmd := exec.CommandContext(t.Context(), "bash", "-c", cleanup)
			fail := "false"
			if fixture.failDelete {
				fail = "true"
			}
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"GITHUB_REPOSITORY=fixture/repo", "PREFIX=lint-", "CURRENT=current", "FIXTURE_KEYS="+fixture.keys,
				"FIXTURE_CALLS="+calls, "FIXTURE_FAIL="+fail)
			output, err := cmd.CombinedOutput()
			if (err != nil) != fixture.wantErr {
				t.Fatalf("cleanup err=%v, want failure %v: %s", err, fixture.wantErr, output)
			}
			deleted, err := os.ReadFile(calls)
			if err != nil && !os.IsNotExist(err) {
				contractcheck.FailErr(t, "read deletions", err)
			}
			if string(deleted) != fixture.deleted || strings.Contains(string(deleted), "current") {
				t.Fatalf("deleted %q, want %q", deleted, fixture.deleted)
			}
		})
	}
}
