package skills

import (
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
)

func TestSkillResolutionTellsTheCardHowItWasChosen(t *testing.T) {
	for exact, want := range map[bool]string{true: "name", false: "engine"} {
		out := &tools.ToolInvocationOut{}
		stateSkillResolution(tools.ToolContext{Effects: tools.InvocationEffects{Out: out}}, exact)
		if len(out.CardFacts) != 1 || out.CardFacts[0].By != want {
			t.Fatalf("exact=%v: %+v", exact, out.CardFacts)
		}
	}
}
