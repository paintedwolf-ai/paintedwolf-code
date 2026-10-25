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

func TestConfineRequestForSpawnRefusesStateAncestorAndKeepsOrdinaryRoot(t *testing.T) {
	_, reject := ConfineRequestForSpawn(t.Context(), ToolContext{}, []string{string(filepath.Separator)})
	if reject == nil || reject.Code != "SANDBOX_CONTROL_PLANE_DENIED" {
		t.Fatalf("reject = %+v, want SANDBOX_CONTROL_PLANE_DENIED", reject)
	}
	if reject.Data["reason"] != confine.WriteRootCodeContainsState {
		t.Fatalf("reject data = %+v, want reason %q", reject.Data, confine.WriteRootCodeContainsState)
	}
	ordinary := t.TempDir()
	req, reject := ConfineRequestForSpawn(t.Context(), ToolContext{}, []string{ordinary})
	if reject != nil || !slices.Contains(req.GrantedWriteRoots, ordinary) {
		t.Fatalf("ordinary root request=%+v rejection=%+v", req, reject)
	}
}
