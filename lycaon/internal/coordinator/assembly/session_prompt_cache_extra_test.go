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
	key := sessionPromptCacheKey(profile, "agents/coordinator-core.md", "", "/tmp", "build", "abc", 1, "prompt-revision")
	cache.StoreStable("s1", key, "cached prompt")
	got, ok := cache.LoadStable("s1", key)
	if !ok || got != "cached prompt" {
		t.Fatalf("cache miss: ok=%v got=%q", ok, got)
	}
	if _, ok := cache.LoadStable("s1", key+"x"); ok {
		t.Fatal("expected miss on key change")
	}
	changedRevision := sessionPromptCacheKey(profile, "agents/coordinator-core.md", "", "/tmp", "build", "abc", 1, "next-prompt-revision")
	if changedRevision == key {
		t.Fatal("prompt source revision must participate in stable cache identity")
	}
}
