//go:build linux && nixworkflow

package confine_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLinuxNixCachedDevelopmentWorkflows(t *testing.T) {
	fixture := prepareNixWorkflows(t)
	const probe = `printf '{"selected":"%s","inherited":"%s","bash":"%s"}\n' "${PW_NIX_SELECTED-}" "${PW_NIX_INHERITED-}" "$BASH"`
	legacy := filepath.Join(filepath.Dir(fixture.bin), "nix-shell")
	command := fixture.bash + " -c '" + strings.ReplaceAll(probe, "'", "'\"'\"'") + "'"
	cases := []struct {
		name, binary, selected, inherited string
		arguments                         []string
	}{
		{"flake develop inherited", fixture.bin, "fixture", "person-value", []string{"develop", "--impure", "--no-write-lock-file", fixture.flake, "--command", fixture.bash, "-c", probe}},
		{"recorded develop pure", fixture.bin, "fixture", "", []string{"develop", "--ignore-env", "--keep-env-var", "HOME", "--keep-env-var", "TMPDIR", "--keep-env-var", "PATH", fixture.profile, "--command", fixture.bash, "-c", probe}},
		{"cached shell", fixture.bin, "", "person-value", []string{"shell", filepath.Dir(filepath.Dir(fixture.bash)), "--command", fixture.bash, "-c", probe}},
		{"flake run", fixture.bin, "", "person-value", []string{"run", "--impure", "--no-write-lock-file", fixture.flake, "--", "-c", probe}},
		{"legacy shell inherited", legacy, "fixture", "person-value", []string{filepath.Join(fixture.project, "shell.nix"), "--run", command}},
		{"legacy shell pure", legacy, "fixture", "", []string{filepath.Join(fixture.project, "shell.nix"), "--pure", "--run", command}},
	}
	for _, row := range cases {
		t.Run(row.name, func(t *testing.T) {
			marker := filepath.Join(fixture.project, "hook-invoked")
			if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
				t.Fatalf("remove prior fixture-owned hook marker: %v", err)
			}
			arguments := append([]string{"--offline", "--store", fixture.store}, row.arguments...)
			if row.binary == legacy {
				arguments = append([]string{"--store", fixture.store}, row.arguments...)
			}
			result, err := fixture.run(t, t.Context(), execpkg.LaunchAgentCommand, row.binary, arguments...)
			var report struct{ Selected, Inherited, Bash string }
			testutil.FailErr(t, "parse selected shell facts", json.Unmarshal(nixResult(t, result, err), &report))
			if report.Selected != row.selected || report.Inherited != row.inherited || report.Bash != fixture.bash {
				t.Fatalf("selected environment=%+v; expected selected=%s inherited=%s bash=%s", report, row.selected, row.inherited, fixture.bash)
			}
			_, markerErr := os.Stat(marker)
			if (row.selected != "" && markerErr != nil) || (row.selected == "" && !os.IsNotExist(markerErr)) {
				t.Fatalf("explicit project hook outcome: %v", markerErr)
			}
		})
	}
}
