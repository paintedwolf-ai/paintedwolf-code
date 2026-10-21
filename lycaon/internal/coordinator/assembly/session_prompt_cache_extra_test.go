package assembly

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSessionPromptCacheStableHit(t *testing.T) {
	var cache SessionPromptCache
	history := []api.Message{{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Hi"}}
	profile := surface.ResolveTurnProfile(api.CoordinatorRunContext{}, &api.Session{Posture: api.SessionPostureBuild}, history)
	key := sessionPromptCacheKey(profile, "agents/coordinator-core.md", "", "/tmp", "build", "abc", "1", "prompt-revision")
	cache.StoreStable("s1", key, "cached prompt")
	got, ok := cache.LoadStable("s1", key)
	if !ok || got != "cached prompt" {
		t.Fatalf("cache miss: ok=%v got=%q", ok, got)
	}
	if _, ok := cache.LoadStable("s1", key+"x"); ok {
		t.Fatal("expected miss on key change")
	}
	changedRevision := sessionPromptCacheKey(profile, "agents/coordinator-core.md", "", "/tmp", "build", "abc", "1", "next-prompt-revision")
	if changedRevision == key {
		t.Fatal("prompt source revision must participate in stable cache identity")
	}
}

func TestStablePromptKeyFollowsEveryRenderedRoot(t *testing.T) {
	vars := func(label, path, scratch, posture string) map[string]any {
		return map[string]any{"root_count": 2, "session_scratch_dir": scratch, "posture": posture, "workspace_roots": []map[string]any{
			{"label": "api", "path": "/abs/api", "is_primary": true},
			{"label": label, "path": path, "is_primary": false},
		}}
	}
	base := rootsFingerprint(vars("web", "/abs/web", "/scratch/s1", "balanced"))
	for name, changed := range map[string]map[string]any{
		"renamed": vars("site", "/abs/web", "/scratch/s1", "balanced"),
		"moved":   vars("web", "/abs/new-web", "/scratch/s1", "balanced"),
		"scratch": vars("web", "/abs/web", "/scratch/s2", "balanced"),
		"posture": vars("web", "/abs/web", "/scratch/s1", "strict"),
	} {
		if rootsFingerprint(changed) == base {
			t.Fatalf("%s root set kept the cached stable prompt", name)
		}
	}
	if rootsFingerprint(vars("web", "/abs/web", "/scratch/s1", "balanced")) != base {
		t.Fatal("identical root set changed the stable prompt key")
	}
}
