package theme

import "testing"

func TestAgentColorsDefaultToTheThemesMutedText(t *testing.T) {
	muted := Color{R: 0x6e, G: 0x6c, B: 0x67, A: 255}
	out, faults := resolveAgentColors(nil, map[string]Color{"text-muted": muted})
	if len(faults) != 0 || out.Main != muted || out.LightnessStep != 0.1 || out.Chroma != 0.03 {
		t.Fatalf("defaults = %+v faults = %v", out, faults)
	}
}

func TestAgentColorsRejectOutOfBoundsRecipes(t *testing.T) {
	step, chroma := 0.5, -0.1
	_, faults := resolveAgentColors(&AgentColorsDeclaration{Main: "#12345680", LightnessStep: &step, Chroma: &chroma}, map[string]Color{})
	fields := map[string]bool{}
	for _, fault := range faults {
		fields[fault.Field] = true
	}
	for _, field := range []string{"agent_colors.main", "agent_colors.lightness_step", "agent_colors.chroma"} {
		if !fields[field] {
			t.Fatalf("missing fault for %s in %v", field, faults)
		}
	}
}
