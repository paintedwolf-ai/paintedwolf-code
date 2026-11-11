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

func TestSpawnPreservesAdmittedWriteRoots(t *testing.T) {
	approved, output := t.TempDir(), t.TempDir()
	tc := ToolContext{Files: InvocationFiles{GrantedWriteRoots: []string{approved}}}
	req, reject := ConfineRequestForSpawn(t.Context(), tc, []string{output, approved})
	if reject != nil || !slices.Equal(req.GrantedWriteRoots, []string{approved, output}) {
		t.Fatalf("spawn roots = %v, rejection = %v", req.GrantedWriteRoots, reject)
	}
	if !slices.Equal(tc.Files.GrantedWriteRoots, []string{approved}) {
		t.Fatalf("admitted roots changed: %v", tc.Files.GrantedWriteRoots)
	}
}

func TestSpawnRevalidatesAdmittedWriteRoots(t *testing.T) {
	tc := ToolContext{Files: InvocationFiles{GrantedWriteRoots: []string{"relative"}}}
	_, reject := ConfineRequestForSpawn(t.Context(), tc, nil)
	if reject == nil || reject.Code != "SANDBOX_CAPABILITY_REQUEST_INVALID" {
		t.Fatalf("unsafe admitted root rejection = %v", reject)
	}
}
