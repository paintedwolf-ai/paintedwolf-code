package contract

import (
	"bytes"
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
	cmd := exec.CommandContext(t.Context(), "go", append([]string{"list", "-deps", "-tags=paintedwolf_release"}, staged...)...)
	cmd.Dir = filepath.Join(root, "lycaon")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list release dependencies: %v\n%s", err, stderr.String())
	}
	for pkg := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		for _, generator := range runtimeCodeGenerators {
			if strings.HasPrefix(pkg, generator) {
				t.Errorf("release build links %s, which generates machine code at run time and is killed under the "+
					"hardened runtime; run that work natively, as the document core does", pkg)
			}
		}
	}
}
