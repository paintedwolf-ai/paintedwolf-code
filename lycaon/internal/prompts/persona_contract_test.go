package prompts

import (
	"strings"
	"testing"
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
