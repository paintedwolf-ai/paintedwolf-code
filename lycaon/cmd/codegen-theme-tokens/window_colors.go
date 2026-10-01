package main

func windowColorsSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"description": "Generative OKLCH family for window cursors, selections, and labels. Omitted anchors derive from the theme; the client enforces display contrast.",
		"properties": map[string]any{
			"main":       map[string]any{"type": "string", "pattern": "^#[0-9a-fA-F]{6}([fF]{2})?$", "description": "Main window identity color; defaults to accent-signal. Display contrast is enforced."},
			"anchors":    map[string]any{"type": "array", "minItems": 1, "maxItems": 12, "items": map[string]any{"type": "string", "pattern": "^#[0-9a-fA-F]{6}([fF]{2})?$"}},
			"hue_spread": map[string]any{"type": "number", "minimum": 0, "maximum": 180, "default": 24, "description": "Maximum hue excursion from each anchor, in degrees."},
			"chroma_min": map[string]any{"type": "number", "minimum": 0, "maximum": 0.4, "default": 0.04, "description": "Lower OKLCH chroma bound; must not exceed chroma_max."},
			"chroma_max": map[string]any{"type": "number", "minimum": 0, "maximum": 0.4, "default": 0.16, "description": "Upper OKLCH chroma bound; gamut and contrast may reduce it."},
		},
	}
}

func windowColorsReference() string {
	return "\n## Window colors\n\n" +
		"`window_colors` declares an open-ended OKLCH color family for active file tabs, peer cursors, selections, and labels. The main window uses `main`; additional native windows use their stable host window number. Removing a window never renumbers or recolors the survivors.\n\n" +
		"| Field | Default | Bounds and meaning |\n|-------|---------|--------------------|\n" +
		"| `main` | `accent-signal` | Opaque hex color for the main window |\n" +
		"| `anchors` | `accent`, `status-positive`, `cost-workers` | 1–12 opaque hex colors defining the peer color family |\n" +
		"| `hue_spread` | 24 | Maximum excursion around each anchor, 0–180 degrees |\n" +
		"| `chroma_min` | 0.04 | Lower OKLCH saturation bound, 0–0.4 |\n" +
		"| `chroma_max` | 0.16 | Upper OKLCH saturation bound, 0–0.4; must be at least `chroma_min` |\n\n" +
		"The client spreads early slots apart by perceptual distance, then continues sampling the recipe deterministically. Colors are mapped into sRGB by reducing chroma; contrast adjustment may further reduce saturation. Carets clear 3:1 against the editor background (4.5:1 with Increase Contrast); labels clear 4.5:1 (7:1 with Increase Contrast). Selection washes preserve body-text contrast up to the same text floor. An authored main color also passes through those display corrections.\n\n" +
		"The recipe generates more colors as needed; stable text labels remain the identifier, including for monochrome recipes and forced colors. Runtime theme and OS contrast changes repaint existing identities without replacing the editor or changing its selection. See the [theme authoring guide](extend.md#themes) for an example.\n\n"
}

func agentColorsSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"description": "Muted OKLCH family for agent chats: one hue, with each chat a lightness step from main. The client enforces display contrast.",
		"properties": map[string]any{
			"main":           map[string]any{"type": "string", "pattern": "^#[0-9a-fA-F]{6}([fF]{2})?$", "description": "Center of the agent family; defaults to text-muted."},
			"lightness_step": map[string]any{"type": "number", "minimum": 0.02, "maximum": 0.25, "default": 0.1, "description": "OKLCH lightness between neighboring chats."},
			"chroma":         map[string]any{"type": "number", "minimum": 0, "maximum": 0.12, "default": 0.03, "description": "OKLCH saturation of every agent color."},
		},
	}
}

func agentColorsReference() string {
	return "## Agent colors\n\n" +
		"`agent_colors` declares the colors of agent chats in files: what they read and what they are about to change. Every chat shares one muted hue; each chat is a lightness step away from `main`. A chat keeps its step while it has presence.\n\n" +
		"| Field | Default | Bounds and meaning |\n|-------|---------|--------------------|\n" +
		"| `main` | `text-muted` | Opaque hex color at the center of the family |\n" +
		"| `lightness_step` | 0.1 | OKLCH lightness between neighboring chats, 0.02–0.25 |\n" +
		"| `chroma` | 0.03 | OKLCH saturation of every agent color, 0–0.12 |\n\n" +
		"Chats alternate lighter and darker around `main`. Carets clear 3:1 against the editor background (4.5:1 with Increase Contrast), and selection washes keep body text legible, so contrast adjustment may move a step. Chat titles on labels remain the identifier.\n\n"
}
