package tools

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
)

func TestConfineRequestForSpawnRefusesUnsafeRoot(t *testing.T) {
	_, reject := ConfineRequestForSpawn(t.Context(), ToolContext{}, []string{string(filepath.Separator)})
	if reject == nil || reject.Code != "SANDBOX_CAPABILITY_REQUEST_INVALID" {
		t.Fatalf("reject = %+v, want SANDBOX_CAPABILITY_REQUEST_INVALID", reject)
	}
	if reject.Data["reason"] != confine.WriteRootCodeFilesystemRoot {
		t.Fatalf("reject data = %+v, want reason %q", reject.Data, confine.WriteRootCodeFilesystemRoot)
	}
}
