package contribution

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/internal/theme"
)

// Theme is one declarative appearance contribution.
type Theme struct {
	ID         string `yaml:"id"`
	Name       string `yaml:"name"`
	Appearance string `yaml:"appearance"`
	// Tokens maps a base-plane token id to a literal color.
	Tokens map[string]string `yaml:"tokens,omitempty"`
	// Syntax maps a scope id to a bare color or a color plus style flags.
	Syntax map[string]SyntaxEntry `yaml:"syntax,omitempty"`
	// Brand is what a theme may say about product identity.
	Brand *ThemeBrand `yaml:"brand,omitempty"`
	// Icons carries stroke treatment and per-slot geometry.
	Icons *ThemeIcons `yaml:"icons,omitempty"`
	// Bevel sets how strongly raised and recessed controls show their edges.
	Bevel        *ThemeBevel                    `yaml:"bevel,omitempty"`
	WindowColors *theme.WindowColorsDeclaration `yaml:"window_colors,omitempty"`
	AgentColors  *theme.AgentColorsDeclaration  `yaml:"agent_colors,omitempty"`
}

// ThemeBevel scales every host-derived bevel edge.
type ThemeBevel struct {
	Strength *float64 `yaml:"strength,omitempty"`
}

// ThemeIcons carries global stroke and per-slot geometry.
type ThemeIcons struct {
	Stroke   *ThemeIconStroke  `yaml:"stroke,omitempty"`
	Geometry map[string]string `yaml:"geometry,omitempty"`
}

// ThemeIconStroke controls stroked slots.
type ThemeIconStroke struct {
	Weight *float64 `yaml:"weight,omitempty"`
	Cap    string   `yaml:"cap,omitempty"`
	Join   string   `yaml:"join,omitempty"`
}

// ThemeBrand controls product mark visibility.
type ThemeBrand struct {
	Logomark string `yaml:"logomark"`
}

// SyntaxEntry is one color with optional style flags.
type SyntaxEntry struct {
	Color     string `yaml:"color"`
	Italic    bool   `yaml:"italic,omitempty"`
	Bold      bool   `yaml:"bold,omitempty"`
	Underline bool   `yaml:"underline,omitempty"`
}

// syntaxEntryFields keeps custom decoding strict.
var syntaxEntryFields = map[string]bool{
	"color": true, "italic": true, "bold": true, "underline": true,
}

// UnmarshalYAML accepts scalar and styled syntax entries.
func (e *SyntaxEntry) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		return node.Decode(&e.Color)
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("a syntax entry is a color or a mapping of color plus style flags")
	}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if !syntaxEntryFields[key] {
			return fmt.Errorf("unknown syntax field %q", key)
		}
	}
	type entry SyntaxEntry
	var out entry
	if err := node.Decode(&out); err != nil {
		return err
	}
	*e = SyntaxEntry(out)
	return nil
}

// Compiled resolves the contribution through the theme vocabulary.
func (t *Theme) Compiled() (theme.Compiled, error) {
	decl := theme.Declaration{
		ID:           t.ID,
		Name:         t.Name,
		Appearance:   theme.Appearance(t.Appearance),
		Tokens:       t.Tokens,
		WindowColors: t.WindowColors,
		AgentColors:  t.AgentColors,
	}
	if t.Brand != nil {
		decl.Logomark = theme.LogomarkVisibility(t.Brand.Logomark)
	}
	if t.Bevel != nil {
		decl.Bevel = &theme.BevelDeclaration{Strength: t.Bevel.Strength}
	}
	if t.Icons != nil {
		decl.Icons = t.Icons.Geometry
		if s := t.Icons.Stroke; s != nil {
			decl.Stroke = &theme.StrokeDeclaration{
				Weight: s.Weight, Cap: s.Cap, Join: s.Join,
			}
		}
	}
	if len(t.Syntax) > 0 {
		decl.Syntax = make(map[string]theme.SyntaxDeclaration, len(t.Syntax))
		for id, entry := range t.Syntax {
			decl.Syntax[id] = theme.SyntaxDeclaration{
				Color:     entry.Color,
				Italic:    entry.Italic,
				Bold:      entry.Bold,
				Underline: entry.Underline,
			}
		}
	}
	return theme.Compile(decl)
}

func validateTheme(t *Theme) error {
	if _, err := t.Compiled(); err != nil {
		return fmt.Errorf("theme %s: %w", t.Name, err)
	}
	return nil
}

// DecodeTheme strictly decodes one theme unit.
func DecodeTheme(body []byte) (*Theme, error) {
	var decl Theme
	if err := decodeStrict(body, &decl); err != nil {
		return nil, err
	}
	return &decl, nil
}
