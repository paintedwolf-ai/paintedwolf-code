package modelfeed

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFeedKeyMapMatchesFixture(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	fixture := filepath.Join(filepath.Dir(thisFile), "testdata", "models_dev_fixture.json")
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	doc, err := ParseDocument(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for kind, key := range feedProviderKeyByKind {
		if _, ok := doc.Providers[key]; !ok {
			t.Fatalf("kind %q maps to feed key %q missing from fixture", kind, key)
		}
	}
}

// Kinds sharing a feed key resolve back to its canonical kind.
func TestKindForFeedKeyRoundTrip(t *testing.T) {
	for kind, key := range feedProviderKeyByKind {
		fk, ok := FeedKeyForKind(kind)
		if !ok || fk != key {
			t.Fatalf("FeedKeyForKind(%q) = %q,%v want %q", kind, fk, ok, key)
		}
		got, ok := KindForFeedKey(key)
		if !ok {
			t.Fatalf("KindForFeedKey(%q) = _,false want a kind", key)
		}
		if back, ok := FeedKeyForKind(got); !ok || back != key {
			t.Fatalf("KindForFeedKey(%q) = %q, which maps to %q — want a kind mapping back to %q", key, got, back, key)
		}
	}
	if _, ok := KindForFeedKey("not-a-provider"); ok {
		t.Fatal("unmapped feed key must be false")
	}
}

func TestVertexExpressMapsToGeminiCatalogNotModelGarden(t *testing.T) {
	got, ok := FeedKeyForKind("vertex-express")
	if !ok {
		t.Fatal("vertex-express must be feed-mapped — express mode has no listing endpoint, so the feed is its only model source")
	}
	if got != "google" {
		t.Fatalf("FeedKeyForKind(vertex-express) = %q, want google", got)
	}
	if got == "google-vertex" {
		t.Error("google-vertex is the whole Model Garden, not Google's Gemini catalog")
	}
}

func TestKindForFeedKeyIsCanonicalWhenShared(t *testing.T) {
	byKey := make(map[string][]string)
	for kind, key := range feedProviderKeyByKind {
		byKey[key] = append(byKey[key], kind)
	}
	for key, kinds := range byKey {
		if len(kinds) < 2 {
			continue
		}
		canonical, ok := canonicalKindByFeedKey[key]
		if !ok {
			t.Fatalf("feed key %q is shared by %v — add a canonicalKindByFeedKey entry", key, kinds)
		}
		got, ok := KindForFeedKey(key)
		if !ok || got != canonical {
			t.Errorf("KindForFeedKey(%q) = %q,%v want canonical %q", key, got, ok, canonical)
		}
	}
	for key := range canonicalKindByFeedKey {
		if len(byKey[key]) == 0 {
			t.Errorf("canonicalKindByFeedKey has %q with no kind mapping to it", key)
		}
	}
}
