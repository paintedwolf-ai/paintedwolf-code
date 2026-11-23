package prompts

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// An archetype names the shared partials a persona is built from; an unknown name
// would render a prompt with none of the platform contract and no error.
func TestPersonaRowMustNameADeclaredArchetype(t *testing.T) {
	merged := PersonaContract{
		Archetypes: map[string]ArchetypeDef{"explore_readonly": {}},
		Agents: map[string]AgentPersonaDef{
			"acme-reader": {Archetype: "not_a_real_archetype"},
		},
	}
	err := validateArchetypeRefs(merged, map[string]string{"acme-reader": "acme/kit"})
	if err == nil {
		t.Fatal("an unknown archetype must fail the contract load")
	}
	for _, want := range []string{"acme-reader", "acme/kit", "not_a_real_archetype"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error should name %q: %v", want, err)
		}
	}
}

func TestPersonaRowWithoutAnArchetypeIsRefused(t *testing.T) {
	merged := PersonaContract{
		Archetypes: map[string]ArchetypeDef{"explore_readonly": {}},
		Agents:     map[string]AgentPersonaDef{"acme-reader": {}},
	}
	if err := validateArchetypeRefs(merged, map[string]string{"acme-reader": "acme/kit"}); err == nil {
		t.Fatal("a row with no archetype must fail the contract load")
	}
}

func TestDeclaredArchetypePasses(t *testing.T) {
	merged := PersonaContract{
		Archetypes: map[string]ArchetypeDef{"explore_readonly": {}},
		Agents: map[string]AgentPersonaDef{
			"acme-reader": {Archetype: "explore_readonly"},
		},
	}
	if err := validateArchetypeRefs(merged, map[string]string{"acme-reader": "acme/kit"}); err != nil {
		t.Fatalf("a declared archetype must load: %v", err)
	}
}

func TestPersonaProcessStepVariantsResolveAgainstReader(t *testing.T) {
	raw := []byte(`
version: 2
agents:
  acme-worker:
    archetype: explore_readonly
    delta:
      role_title: Acme Worker
      focus: Inspect safely.
      process:
        - "First plain step."
        - variants:
            - web_search: true
              tools: [web_search, fetch_url]
              text: "Use web_search and fetch_url."
            - tools: [command]
              text: "Fallback to command when web search is disabled."
`)
	var cfg PersonaContract
	if err := config.DecodeYAML(raw, &cfg); err != nil {
		t.Fatalf("DecodeYAML: %v", err)
	}
	def := cfg.Agents["acme-worker"]
	defaultMap := PersonaDataMapForReader(def, Reader{})
	if !strings.Contains(defaultMap["process"].(string), "Use web_search and fetch_url.") {
		t.Fatalf("PersonaDataMapForReader default = %q", defaultMap["process"])
	}

	profiles, err := sandbox.LoadToolProfiles()
	if err != nil {
		t.Fatalf("LoadToolProfiles: %v", err)
	}
	noWebReader, err := NewReader(ReaderInput{
		ProfileID: "explore_readonly",
		Profiles:  profiles,
		Visible:   []string{"command"},
		RootCount: 1,
		WebSearch: false,
	})
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	forReader := PersonaDataMapForReader(def, noWebReader)
	if strings.Contains(forReader["process"].(string), "web_search") || !strings.Contains(forReader["process"].(string), "Fallback to command") {
		t.Fatalf("PersonaDataMapForReader(noWeb) = %q", forReader["process"])
	}
}
