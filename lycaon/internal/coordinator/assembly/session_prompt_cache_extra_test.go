package assembly

import (
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
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
	profiles, err := sandbox.LoadToolProfiles()
	if err != nil {
		t.Fatalf("LoadToolProfiles: %v", err)
	}
	plan, err := prompts.CompileSurfacePlan("implement_routing", 2)
	if err != nil {
		t.Fatalf("CompileSurfacePlan: %v", err)
	}
	key := func(label, path, scratch string, reviewsConfinedWrites, externalHidden bool) string {
		t.Helper()
		reader, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID:             prompts.CoordinatorProfileID,
			SurfaceID:             "implement_routing",
			Profiles:              profiles,
			Plan:                  plan,
			RootCount:             2,
			WebSearch:             true,
			SessionScratchDir:     scratch,
			ReviewsConfinedWrites: reviewsConfinedWrites,
			CommandExternalHidden: externalHidden,
		})
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		v := map[string]any{
			"workspace_roots": []map[string]any{
				{"label": "api", "path": "/abs/api", "is_primary": true},
				{"label": label, "path": path, "is_primary": false},
			},
		}
		in := standingPromptInputs{
			PromptRevision: "prompt-revision",
			Profile:        standingProfileInputs{SurfaceID: "implement_routing"},
			Vars:           v,
			RootCount:      2,
		}.withReader(reader)
		k, err := in.cacheKey()
		if err != nil {
			t.Fatalf("cacheKey: %v", err)
		}
		return k
	}
	base := key("web", "/abs/web", "/scratch/s1", false, false)
	for name, changed := range map[string]string{
		"renamed":                 key("site", "/abs/web", "/scratch/s1", false, false),
		"moved":                   key("web", "/abs/new-web", "/scratch/s1", false, false),
		"scratch":                 key("web", "/abs/web", "/scratch/s2", false, false),
		"reviews_confined_writes": key("web", "/abs/web", "/scratch/s1", true, false),
		"command_external_hidden": key("web", "/abs/web", "/scratch/s1", false, true),
	} {
		if changed == base {
			t.Fatalf("%s root set kept the cached stable prompt", name)
		}
	}
	if key("web", "/abs/web", "/scratch/s1", false, false) != base {
		t.Fatal("identical root set changed the stable prompt key")
	}
}
