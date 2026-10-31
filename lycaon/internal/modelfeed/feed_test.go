package modelfeed

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRefreshAndDocumentDiskCache(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "models_dev_fixture.json"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	dir := t.TempDir()
	f, err := New(Options{
		URL:      DefaultURL,
		GetBytes: func(context.Context, string) ([]byte, error) { return raw, nil },
		CacheDir: dir,
		TTL:      time.Hour,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if f.Usable() {
		t.Fatal("expected no document before refresh")
	}
	if f.Status() != StatusUnavailable {
		t.Fatalf("status = %q, want unavailable", f.Status())
	}
	refreshPublications := 0
	f.AddRefreshListener(func() { refreshPublications++ })

	doc, err := f.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if doc == nil || !f.Usable() {
		t.Fatal("expected usable document after refresh")
	}
	if f.Status() != StatusOK {
		t.Fatalf("status = %q, want ok", f.Status())
	}
	if refreshPublications != 1 {
		t.Fatalf("refresh publications = %d, want 1", refreshPublications)
	}
	if _, err := os.Stat(filepath.Join(dir, "api.json")); err != nil {
		t.Fatalf("cache missing: %v", err)
	}

	f2, err := New(Options{
		URL:      "https://models.dev/api.json",
		CacheDir: dir,
		TTL:      time.Hour,
		GetBytes: func(context.Context, string) ([]byte, error) {
			return nil, errors.New("network should not run")
		},
	})
	if err != nil {
		t.Fatalf("New from cache: %v", err)
	}
	if !f2.Usable() {
		t.Fatal("expected last-good cache to load")
	}
	got, err := f2.Document(context.Background())
	if err != nil || got == nil {
		t.Fatalf("Document from cache: doc=%v err=%v", got, err)
	}
	if _, ok := got.Providers["openai"]; !ok {
		t.Fatal("expected openai in cached document")
	}
}

func TestDocumentUnavailableWithoutCache(t *testing.T) {
	f, err := New(Options{
		CacheDir: t.TempDir(),
		TTL:      time.Hour,
		GetBytes: func(context.Context, string) ([]byte, error) {
			return nil, errors.New("offline")
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	doc, err := f.Document(context.Background())
	if doc != nil {
		t.Fatalf("expected nil document, got providers=%d", len(doc.Providers))
	}
	if err == nil {
		t.Fatal("expected error when never-fetched")
	}
	if f.Status() != StatusUnavailable {
		t.Fatalf("status = %q", f.Status())
	}
}

func TestRefreshIgnoresCostTrackingSetting(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "models_dev_fixture.json"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	f, err := New(Options{
		CacheDir: t.TempDir(),
		GetBytes: func(context.Context, string) ([]byte, error) { return raw, nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := f.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !f.Usable() {
		t.Fatal("refresh must succeed without any cost settings")
	}
}

func TestFixtureProviderKeysPresent(t *testing.T) {
	doc := loadFixtureDoc(t)
	want := []string{
		"openai", "anthropic", "togetherai", "openrouter", "fireworks-ai",
		"amazon-bedrock", "google-vertex", "google", "azure", "cloudflare-workers-ai",
		"mistral",
	}
	for _, key := range want {
		if _, ok := doc.Providers[key]; !ok {
			t.Fatalf("fixture missing provider key %q", key)
		}
	}
	var root map[string]json.RawMessage
	raw, _ := os.ReadFile(filepath.Join("testdata", "models_dev_fixture.json"))
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("fixture json: %v", err)
	}
	if len(root) != len(want) {
		t.Fatalf("fixture providers = %d, want %d", len(root), len(want))
	}
}

func TestMistralRecordsFilterNonConversationModels(t *testing.T) {
	doc := loadFixtureDoc(t)
	feedKey, ok := FeedKeyForKind("mistral")
	if !ok || feedKey != "mistral" {
		t.Fatalf("FeedKeyForKind(mistral) = %q, %v", feedKey, ok)
	}
	models := doc.EligibleModels(feedKey)
	if len(models) == 0 {
		t.Fatal("EligibleModels(mistral) returned 0 models")
	}
	byID := make(map[string]Model, len(models))
	for _, m := range models {
		byID[m.ID] = m
	}
	if _, exists := byID["mistral-embed"]; exists {
		t.Fatal("mistral-embed should be excluded from conversation models")
	}
	large, ok := byID["mistral-large-latest"]
	if !ok {
		t.Fatal("missing mistral-large-latest")
	}
	if large.Limit == nil || large.Limit.Context != 131072 || large.Limit.Output != 32768 {
		t.Fatalf("mistral-large-latest limit = %+v", large.Limit)
	}
	if large.ToolCall == nil || !*large.ToolCall {
		t.Fatalf("mistral-large-latest tool_call = %v", large.ToolCall)
	}
	magistral, ok := byID["magistral-medium-latest"]
	if !ok || magistral.Reasoning == nil || !*magistral.Reasoning {
		t.Fatalf("magistral-medium-latest reasoning = %+v", magistral)
	}
	pixtral, ok := byID["pixtral-large-latest"]
	if !ok || !pixtral.Attachment {
		t.Fatalf("pixtral-large-latest should report attachment support: %+v", pixtral)
	}
}
