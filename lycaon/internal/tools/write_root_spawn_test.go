package tools

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
)

func TestConfineRequestForSpawnRefusesUnsafeRoot(t *testing.T) {
	_, reject := ConfineRequestForSpawn(t.Context(), ToolContext{}, []string{"relative"})
	if reject == nil || reject.Code != "SANDBOX_CAPABILITY_REQUEST_INVALID" {
		t.Fatalf("reject = %+v, want SANDBOX_CAPABILITY_REQUEST_INVALID", reject)
	}
	if reject.Data["reason"] != confine.WriteRootCodeNotAbsolute {
		t.Fatalf("reject data = %+v, want reason %q", reject.Data, confine.WriteRootCodeNotAbsolute)
	}
}

func TestConfineRequestForSpawnKeepsExplicitFilesystemRoot(t *testing.T) {
	req, reject := ConfineRequestForSpawn(t.Context(), ToolContext{}, []string{string(filepath.Separator)})
	if reject != nil || !slices.Contains(req.GrantedWriteRoots, string(filepath.Separator)) {
		t.Fatalf("root request=%+v rejection=%+v", req, reject)
	}
}
