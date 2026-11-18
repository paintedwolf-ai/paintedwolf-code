package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// The no-folder row is not selectable.
func TestNoFolderAllowlistIsNotASurface(t *testing.T) {
	t.Parallel()
	if label := surface.LoadCoordinatorSurfaceActivityLabel("no_folder_allowlist"); label != "" {
		t.Fatalf("no_folder_allowlist has activity_label %q; it is an allow-list, not a surface", label)
	}
	surfaces, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "compile coordinator surfaces", err)
	if _, ok := surfaces["no_folder_allowlist"]; ok {
		t.Fatal("no_folder_allowlist appears in the surface set")
	}
}
