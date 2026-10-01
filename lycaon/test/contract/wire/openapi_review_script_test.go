package contract

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestOpenAPIReviewReport(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		stdout        string
		stderr        string
		toolStatus    int
		installStatus int
		missing       string
		wantStatus    int
	}{
		{name: "unchanged"},
		{name: "breaking changes", stdout: "error: endpoint removed\n"},
		{name: "invalid specification", stderr: "cannot parse specification\n", toolStatus: 2, wantStatus: 2},
		{name: "tool failure", stderr: "report unavailable\n", toolStatus: 7, wantStatus: 7},
		{name: "installation failure", installStatus: 37, wantStatus: 37},
		{name: "missing baseline", missing: "docs/openapi/baseline-bundle.yaml", wantStatus: 1},
		{name: "missing bundle", missing: "docs/openapi.yaml", wantStatus: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root, paths, inputs := openAPIReviewFixture(t)
			if tc.missing != "" {
				contractcheck.FailErr(t, "remove report input", os.Remove(filepath.Join(root, tc.missing)))
			}
			stdout, stderr, status := runOpenAPIReview(t, root, paths,
				"REPORT_STDOUT="+tc.stdout, "REPORT_STDERR="+tc.stderr,
				"REPORT_STATUS="+strconv.Itoa(tc.toolStatus),
				"INSTALL_STATUS="+strconv.Itoa(tc.installStatus))
			if status != tc.wantStatus {
				t.Fatalf("report status = %d, want %d; stdout=%q stderr=%q", status, tc.wantStatus, stdout, stderr)
			}
			if !strings.HasSuffix(stdout, tc.stdout) || !strings.HasSuffix(stderr, tc.stderr) {
				t.Fatalf("tool output lost: stdout=%q stderr=%q", stdout, stderr)
			}
			if tc.missing != "" && !strings.Contains(stderr, tc.missing) {
				t.Errorf("missing input not identified: %q", stderr)
			}
			if tc.installStatus != 0 && !strings.Contains(stderr, "installation unavailable") {
				t.Errorf("installation diagnostic lost: %q", stderr)
			}
			_, err := os.Stat(filepath.Join(paths.Bin, "oasdiff.version"))
			if stamped := !os.IsNotExist(err); stamped != (tc.missing == "" && tc.installStatus == 0) {
				t.Errorf("installation stamp present=%v after preparation: %v", stamped, err)
			}
			for path, want := range inputs {
				got, err := os.ReadFile(filepath.Join(root, path))
				if path == tc.missing {
					if !os.IsNotExist(err) {
						t.Errorf("report recreated missing input %s: %v", path, err)
					}
					continue
				}
				contractcheck.FailErr(t, "read report input", err)
				if string(got) != want {
					t.Errorf("report modified %s", path)
				}
			}
		})
	}
}

func TestOpenAPIReviewReusesInstalledTool(t *testing.T) {
	t.Parallel()
	root, paths, _ := openAPIReviewFixture(t)
	for _, installStatus := range []string{"0", "37"} {
		stdout, stderr, status := runOpenAPIReview(t, root, paths, "INSTALL_STATUS="+installStatus)
		if status != 0 {
			t.Fatalf("cached report status = %d; stdout=%q stderr=%q", status, stdout, stderr)
		}
	}
}

func openAPIReviewFixture(t *testing.T) (string, contractcheck.ArtifactPaths, map[string]string) {
	t.Helper()
	scratch := t.TempDir()
	root := filepath.Join(scratch, "review checkout")
	paths := contractcheck.InstallArtifactPaths(t, root, filepath.Join(scratch, "user cache"))
	inputs := map[string]string{
		"docs/openapi.yaml":                 "current bundle\n",
		"docs/openapi/baseline-bundle.yaml": "previous bundle\n",
	}
	files := map[string]string{
		"scripts/diff-openapi.sh": contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "scripts/diff-openapi.sh"),
		"lycaon/go.mod":           "module fixture\n\ngo 1.26.0\n",
		"bin/go": `#!/usr/bin/env bash
set -euo pipefail
if [[ "${INSTALL_STATUS:-0}" != 0 ]]; then
  echo 'installation unavailable' >&2
  exit "$INSTALL_STATUS"
fi
[[ "$1" == install && "$2" == github.com/oasdiff/oasdiff@v* ]]
cp ../fixture-oasdiff "$GOBIN/oasdiff"
`,
		"fixture-oasdiff": `#!/usr/bin/env bash
set -euo pipefail
[[ "$#" == 5 && "$1" == changelog && "$2" == --level && "$3" == ERR && "$4" == docs/openapi/baseline-bundle.yaml && "$5" == docs/openapi.yaml ]]
printf '%s' "${REPORT_STDOUT:-}"
printf '%s' "${REPORT_STDERR:-}" >&2
exit "${REPORT_STATUS:-0}"
`,
	}
	for path, body := range inputs {
		files[path] = body
	}
	for path, body := range files {
		absolute := filepath.Join(root, path)
		contractcheck.FailErr(t, "create review fixture directory", os.MkdirAll(filepath.Dir(absolute), 0o700))
		contractcheck.FailErr(t, "write review fixture", os.WriteFile(absolute, []byte(body), 0o700))
	}
	return root, paths, inputs
}

func runOpenAPIReview(t *testing.T, root string, paths contractcheck.ArtifactPaths, env ...string) (string, string, int) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(root, "scripts/diff-openapi.sh"))
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Env = append(cmd.Env, paths.Env()...)
	cmd.Env = append(cmd.Env, env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	var exitErr *exec.ExitError
	if err := cmd.Run(); err != nil && !errors.As(err, &exitErr) {
		contractcheck.FailErr(t, "run review script", err)
	}
	return stdout.String(), stderr.String(), cmd.ProcessState.ExitCode()
}
