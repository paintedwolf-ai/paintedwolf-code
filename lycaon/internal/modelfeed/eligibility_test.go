package modelfeed

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func loadFixtureDoc(t *testing.T) *Document {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "models_dev_fixture.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	doc, err := parseDocument(raw, time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC), DefaultURL)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return doc
}

func TestConversationEligibleFixtureRows(t *testing.T) {
	openai := loadFixtureDoc(t).Providers["openai"].Models

	for _, id := range []string{"gpt-4.1", "gpt-4.1-mini", "o3"} {
		m, ok := openai[id]
		if !ok {
			t.Fatalf("fixture missing %s", id)
		}
		if !ConversationEligible(m) {
			t.Fatalf("%s should be ConversationEligible", id)
		}
	}

	for _, id := range []string{"text-embedding-3-small", "gpt-image-1", "synthetic-embed-temp"} {
		m, ok := openai[id]
		if !ok {
			t.Fatalf("fixture missing %s", id)
		}
		if ConversationEligible(m) {
			t.Fatalf("%s must not be ConversationEligible", id)
		}
	}
}

func TestEligibleModelsDropsNonChat(t *testing.T) {
	ids := map[string]bool{}
	for _, m := range loadFixtureDoc(t).EligibleModels("openai") {
		ids[m.ID] = true
	}
	if !ids["gpt-4.1"] || !ids["o3"] {
		t.Fatalf("expected chat models kept, got %v", ids)
	}
	if ids["text-embedding-3-small"] || ids["gpt-image-1"] || ids["synthetic-embed-temp"] {
		t.Fatalf("expected embeds/image dropped, got %v", ids)
	}
}

func TestConversationEligibleKeepsFreeAndUnpricedChatModels(t *testing.T) {
	for _, model := range []Model{
		{ID: "free", Family: "nemotron", ToolCall: new(true), Modalities: Modalities{Output: []string{"text"}}, Cost: &Cost{}},
		{ID: "unpriced", Family: "gemma", Reasoning: new(true), Modalities: Modalities{Output: []string{"text"}}},
	} {
		if !ConversationEligible(model) {
			t.Fatalf("%s should remain eligible without a positive price", model.ID)
		}
	}
}
