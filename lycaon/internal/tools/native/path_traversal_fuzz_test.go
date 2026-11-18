package native

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMultiRootResolveAbsNeverEscapesUnion(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "a")
	secondary := filepath.Join(base, "b")
	for _, dir := range []string{primary, secondary} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			testutil.FailErr(t, "create directory", err)
		}
	}
	roots := []projectroot.RootRef{
		{ID: "p", Label: "a", Path: primary, IsPrimary: true},
		{ID: "s", Label: "b", Path: secondary, IsPrimary: false},
	}
	evil := []string{"../outside.txt", "../../etc/passwd", "@missing/x", "../../../tmp"}
	for _, path := range evil {
		_, _, err := projectroot.ResolveAbs(roots, "p", path)
		if err == nil {
			t.Fatalf("ResolveAbs(%q) should fail", path)
		}
	}
	abs, _, err := projectroot.ResolveAbs(roots, "p", "@b/ok.txt")
	if err != nil {
		t.Fatalf("ResolveAbs labeled path: %v", err)
	}
	if !strings.HasPrefix(abs, secondary) {
		t.Fatalf("abs = %q want under secondary", abs)
	}
}
