package prompts

import (
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// Every worker persona render hands the template the reader's value for every
// reader fact, under each command-effect posture and root count.
func TestPersonaRenderDataCarriesReaderFacts(t *testing.T) {
	eff, err := extpacks.CatalogForConsumers()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	agents, err := agentdef.LoadEffectiveWithCatalog(eff)
	if err != nil {
		t.Fatalf("load agents: %v", err)
	}
	shipped, err := sandbox.LoadToolProfilesWithCatalog(eff)
	if err != nil {
		t.Fatalf("load tool profiles: %v", err)
	}
	engine := NewFileTemplateEngineLayers(PromptLayers{ModuleRoot: configlayout.FindModuleRoot()})
	all := toolcontract.AllCommandEffects()
	for posture := toolcontract.CommandEffects(0); posture <= all; posture++ {
		if posture&^all != 0 {
			continue
		}
		profiles := slices.Clone(shipped)
		for i := range profiles {
			profiles[i].CommandEffects &= posture
		}
		for _, rootCount := range []int{0, 1, 2} {
			for _, agent := range agents {
				if agent.ID == "coordinator" {
					continue
				}
				t.Run(fmt.Sprintf("effects=%d/roots=%d/%s", posture, rootCount, agent.ID), func(t *testing.T) {
					reader, err := NewReader(ReaderInput{
						ProfileID: agent.ToolProfile,
						Profiles:  profiles,
						RootCount: rootCount,
						HasGit:    rootCount > 0,
						WebSearch: true,
					})
					if err != nil {
						t.Fatalf("reader: %v", err)
					}
					_, _, _, data, err := personaVars(t.Context(), engine, agent.ID, reader, nil)
					if err != nil {
						t.Fatalf("persona vars: %v", err)
					}
					for key, want := range reader.Facts() {
						if got, ok := data[key]; !ok || !reflect.DeepEqual(got, want) {
							t.Errorf("%s = %#v (present %t), want %#v", key, got, ok, want)
						}
					}
				})
			}
		}
	}
}
