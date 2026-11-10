package profiles

import (
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
)

func TestPromptSurfaceFingerprintTracksSkillAvailability(t *testing.T) {
	first := prompts.AgentPromptSurface{Skills: []prompts.AgentSkillView{{Name: "first", Description: "First procedure"}}}
	second := prompts.AgentPromptSurface{Skills: []prompts.AgentSkillView{{Name: "second", Description: "Second procedure"}}}
	if first.CacheFingerprint() != second.CacheFingerprint() {
		t.Fatal("skill catalog changes that do not alter prompt text should keep the prefix")
	}
	if first.CacheFingerprint() == (prompts.AgentPromptSurface{}).CacheFingerprint() {
		t.Fatal("skill availability changes the prompt procedure")
	}
}

func TestFullCatalogFingerprintTracksRenderedCards(t *testing.T) {
	first := prompts.AgentPromptSurface{FullCatalog: true, Skills: []prompts.AgentSkillView{{Name: "review", Description: "Review code"}}}
	second := first
	second.Skills = []prompts.AgentSkillView{{Name: "review", Description: "Review accessibility"}}
	if first.CacheFingerprint() == second.CacheFingerprint() {
		t.Fatal("changed card reused prompt identity")
	}
	second = first
	second.FullCatalog = false
	if first.CacheFingerprint() == second.CacheFingerprint() {
		t.Fatal("catalog visibility reused prompt identity")
	}
}
