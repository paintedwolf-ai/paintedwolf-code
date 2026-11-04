package contract

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Shipped Go executables run under the hardened runtime with no
// executable-memory entitlement, so the kernel kills one the first time it
// executes generated machine code. These modules generate it.
var runtimeCodeGenerators = []string{
	"github.com/tetratelabs/wazero",
	"github.com/wasilibs/",
}

func TestShippedGoBinariesLinkNoRuntimeCodeGenerator(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	staged := stagedGoPackages(t, contractcheck.ReadRepoFile(t, root, "scripts/stage-engine.sh"))
	if len(staged) == 0 {
		t.Fatal("scripts/stage-engine.sh: no `go build ./cmd/...` invocations found — the extraction pattern is stale")
	}
	for _, goos := range []string{"darwin", "linux"} {
		for pkg := range strings.SplitSeq(goList(t, root, goos, append([]string{"-deps", "-tags=paintedwolf_release"}, stagedForOS(t, root, goos, staged)...)...), "\n") {
			for _, generator := range runtimeCodeGenerators {
				if strings.HasPrefix(pkg, generator) {
					t.Errorf("%s release build links %s, which generates machine code at run time and is killed under the "+
						"hardened runtime; run that work natively, as the document core does", goos, pkg)
				}
			}
		}
	}
}

// stagedForOS keeps the staged commands that have source on the platform, since
// staging builds some only for Linux.
func stagedForOS(t *testing.T, root, goos string, staged []string) []string {
	t.Helper()
	listed := goList(t, root, goos, append([]string{"-e", "-f", "{{if .GoFiles}}{{.ImportPath}}{{end}}"}, staged...)...)
	return strings.Fields(listed)
}

func goList(t *testing.T, root, goos string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", append([]string{"list"}, args...)...)
	cmd.Dir = filepath.Join(root, "lycaon")
	cmd.Env = append(os.Environ(), "GOOS="+goos)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list %s release dependencies: %v\n%s", goos, err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}
