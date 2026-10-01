package theme

import (
	"fmt"
	"math"
)

// AgentColors is the hue family for agent chats; each chat steps in lightness around Main.
type AgentColors struct {
	Main          Color
	LightnessStep float64
	Chroma        float64
}

// AgentColorsDeclaration is the authored agent color recipe; omitted fields derive from the theme.
type AgentColorsDeclaration struct {
	Main          string   `yaml:"main,omitempty"`
	LightnessStep *float64 `yaml:"lightness_step,omitempty"`
	Chroma        *float64 `yaml:"chroma,omitempty"`
}

// Agent color bounds keep chats distinguishable without competing with window colors.
const (
	agentLightnessStepMin = 0.02
	agentLightnessStepMax = 0.25
	agentChromaMax        = 0.12
)

// DefaultAgentColors is the family a theme gets when it declares none.
func DefaultAgentColors(c Compiled) AgentColors {
	out, _ := resolveAgentColors(nil, c.Tokens)
	return out
}

func resolveAgentColors(decl *AgentColorsDeclaration, tokens map[string]Color) (AgentColors, []Fault) {
	out := AgentColors{Main: tokens["text-muted"], LightnessStep: 0.1, Chroma: 0.03}
	if decl == nil {
		return out, nil
	}
	var faults []Fault
	if decl.Main != "" {
		color, err := ParseColor(decl.Main)
		if err != nil || color.A != 255 {
			faults = append(faults, Fault{Field: "agent_colors.main", Message: "expected an opaque hex color"})
		} else {
			out.Main = color
		}
	}
	if v := decl.LightnessStep; v != nil {
		if math.IsNaN(*v) || math.IsInf(*v, 0) || *v < agentLightnessStepMin || *v > agentLightnessStepMax {
			faults = append(faults, Fault{Field: "agent_colors.lightness_step", Message: fmt.Sprintf("must be finite and between %g and %g", agentLightnessStepMin, agentLightnessStepMax)})
		} else {
			out.LightnessStep = *v
		}
	}
	if v := decl.Chroma; v != nil {
		if math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0 || *v > agentChromaMax {
			faults = append(faults, Fault{Field: "agent_colors.chroma", Message: fmt.Sprintf("must be finite and between 0 and %g", agentChromaMax)})
		} else {
			out.Chroma = *v
		}
	}
	return out, faults
}
