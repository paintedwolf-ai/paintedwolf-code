package assembly

import (
	"testing"
)

func TestSessionPromptCacheStableHit(t *testing.T) {
	var cache SessionPromptCache
	in := standingPromptInputs{
		PromptRevision: "prompt-revision",
		Profile:        standingProfileInputs{SurfaceID: "implement_routing"},
		RootCount:      1,
	}
	key, err := in.cacheKey()
	if err != nil {
		t.Fatalf("cacheKey: %v", err)
	}
	cache.StoreStable("s1", key, "cached prompt")
	got, ok := cache.LoadStable("s1", key)
	if !ok || got != "cached prompt" {
		t.Fatalf("cache miss: ok=%v got=%q", ok, got)
	}
	if _, ok := cache.LoadStable("s1", key+"x"); ok {
		t.Fatal("expected miss on key change")
	}
	in2 := in
	in2.PromptRevision = "next-prompt-revision"
	changedRevision, err := in2.cacheKey()
	if err != nil {
		t.Fatalf("cacheKey changed: %v", err)
	}
	if changedRevision == key {
		t.Fatal("prompt source revision must participate in stable cache identity")
	}
}

func TestStablePromptKeyFollowsEveryRenderedRoot(t *testing.T) {
	vars := func(label, path, scratch string, reviewsConfinedWrites, externalHidden bool) map[string]any {
		return map[string]any{"root_count": 2, "session_scratch_dir": scratch, "reviews_confined_writes": reviewsConfinedWrites, "command_external_hidden": externalHidden, "workspace_roots": []map[string]any{
			{"label": "api", "path": "/abs/api", "is_primary": true},
			{"label": label, "path": path, "is_primary": false},
		}}
	}
	key := func(v map[string]any) string {
		t.Helper()
		in := standingPromptInputs{PromptRevision: "prompt-revision", Profile: standingProfileInputs{SurfaceID: "implement_routing"}, Vars: v, RootCount: 2}
		k, err := in.cacheKey()
		if err != nil {
			t.Fatalf("cacheKey: %v", err)
		}
		return k
	}
	base := key(vars("web", "/abs/web", "/scratch/s1", false, false))
	for name, changed := range map[string]map[string]any{
		"renamed":                 vars("site", "/abs/web", "/scratch/s1", false, false),
		"moved":                   vars("web", "/abs/new-web", "/scratch/s1", false, false),
		"scratch":                 vars("web", "/abs/web", "/scratch/s2", false, false),
		"reviews_confined_writes": vars("web", "/abs/web", "/scratch/s1", true, false),
		"command_external_hidden": vars("web", "/abs/web", "/scratch/s1", false, true),
	} {
		if key(changed) == base {
			t.Fatalf("%s root set kept the cached stable prompt", name)
		}
	}
	if key(vars("web", "/abs/web", "/scratch/s1", false, false)) != base {
		t.Fatal("identical root set changed the stable prompt key")
	}
}
