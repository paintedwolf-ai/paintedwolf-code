package confine_test

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
)

func TestOutputCapabilityGrantsReadWithoutWriteAuthority(t *testing.T) {
	confine.TestingSetAutoConfine(t)
	state, project := t.TempDir(), t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", state)
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")
	output := filepath.Join(state, "session-output", "chat")
	c, ok := confine.DefaultConfinement(confine.Request{Roots: []string{project}, SessionOutputRoot: output})
	if !ok || c == nil {
		t.Fatal("ordinary confinement unavailable")
	}
	if !slices.Contains(c.ReadRoots, output) || slices.Contains(c.Roots, output) || slices.Contains(c.GrantedWriteRoots, output) {
		t.Fatalf("capability roots=%+v", c)
	}
	path := fspath.CanonicalPath(filepath.Join(output, "program.stdout"))
	rules := confine.BoundaryOf(c).Filesystem
	if !rules.Verdict(confine.AccessRead, path).Allowed || rules.Verdict(confine.AccessWrite, path).Allowed {
		t.Fatalf("read/write verdict=%+v/%+v", rules.Verdict(confine.AccessRead, path), rules.Verdict(confine.AccessWrite, path))
	}
	if refused, code := confine.GrantedWriteRootRefused(output); !refused || code != confine.WriteRootCodeControlPlane {
		t.Fatalf("output write grant=%v/%s", refused, code)
	}
}
