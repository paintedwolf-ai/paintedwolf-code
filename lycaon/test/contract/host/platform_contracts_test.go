package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/nativemanifest"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// command runs outside the native tool and operation groups; git operations
// are native operations.
func TestNativeToolsManifest(t *testing.T) {
	t.Parallel()
	manifest, err := nativemanifest.Load()
	contractcheck.FailErr(t, "load native manifests", err)
	if len(manifest.NativeToolNames()) == 0 || len(manifest.AllOperations()) == 0 {
		t.Fatal("native manifests declare no tools or operations")
	}
	if manifest.HasTool("command") || manifest.HasOperation("command") {
		t.Fatal("native manifests list command")
	}
	if len(manifest.OperationGroup("git")) == 0 {
		t.Fatal("native manifests declare no git operations")
	}
}
