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
