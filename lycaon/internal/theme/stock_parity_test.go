package theme

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// Stock palette anchors cover each visual family.
func TestStockThemeAnchorsAreStable(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		tokens map[string]string
		syntax map[string]string
	}{
		"Daylight": {
			tokens: map[string]string{
				"background": "#ffffff", "surface": "#ffffff", "surface-inset": "#ffffff",
				"border": "#d3d2cf", "selection": "#f8efeb", "selection-strong": "#f2e2db",
				"brand-field": "#1c1814",
				"text":        "#1c1b1a", "text-muted": "#6e6c67",
				"accent": "#b85c38", "accent-text": "#9d4e2b", "on-accent": "#ffffff",
				"status-positive": "#5d7a55", "status-running": "#c87941",
				"danger": "#c2453f", "warning": "#c97a00",
				// Added lines take their own green; removals keep danger.
				"diff-add-hue": "#2d8a4e", "diff-delete-hue": "#c2453f",
				"draft-accent": "#7a9e6a", "draft-accent-text": "#727d68",
				// Stock edges at the default strength.
				"bevel-highlight": "#ffffff", "bevel-shadow": "#1c1b1a21",
				"bevel-plate-highlight": "#ffffff52",
			},
			syntax: map[string]string{
				"keyword": "#9a1f73", "string": "#0a3069", "constant": "#0550ae",
				"function": "#6639ba", "type": "#953800", "property": "#0550ae",
				"comment": "#525c67", "meta": "#525c67", "tag": "#116329",
				// Specialized constants keep the stock constant color.
				"keyword.self": "#0550ae", "string.escape": "#0550ae",
				"variable.special": "#0550ae",
				// Never painted: these fall through to body ink.
				"variable": "#1c1b1a", "punctuation": "#1c1b1a",
			},
		},
		"Charcoal": {
			tokens: map[string]string{
				"background": "#191817", "surface": "#191817", "border": "#383633",
				"brand-field": "#1c1814",
				"text":        "#e4e2df", "text-muted": "#a3a09b",
				"accent": "#a85830", "accent-text": "#d99263", "on-accent": "#ffffff",
				"status-positive": "#7a9e6a", "status-running": "#c87941",
				"danger": "#e07070", "warning": "#e0a020",
				"diff-add-hue": "#57ab5a", "diff-delete-hue": "#e07070",
				"draft-accent": "#7a9e6a", "draft-accent-text": "#989f8d",
				"bevel-highlight": "#e4e2df14", "bevel-shadow": "#00000042",
				"bevel-plate-highlight": "#ffffff33",
			},
			syntax: map[string]string{
				"keyword": "#f692ce", "string": "#a5d6ff", "constant": "#79c0ff",
				"function": "#d2a8ff", "type": "#ffa657", "property": "#79c0ff",
				"comment": "#adb4bd", "meta": "#adb4bd", "tag": "#7ee787",
				"keyword.self": "#79c0ff", "string.escape": "#79c0ff",
				"variable.special": "#79c0ff",
				"variable":         "#e4e2df", "punctuation": "#e4e2df",
			},
		},
	}

	for _, compiled := range loadStockForTest(t) {
		want, ok := cases[compiled.Name]
		if !ok {
			t.Errorf("stock theme %q has no anchor set; add one", compiled.Name)
			continue
		}
		for id, hex := range want.tokens {
			if got := compiled.Tokens[id].String(); got != hex {
				t.Errorf("%s: token %s = %s, shipped %s", compiled.Name, id, got, hex)
			}
		}
		for id, hex := range want.syntax {
			if got := compiled.Syntax[id].Color.String(); got != hex {
				t.Errorf("%s: syntax %s = %s, shipped %s", compiled.Name, id, got, hex)
			}
		}
		// Folded prose scopes keep italic styling.
		for _, id := range []string{"comment", "comment.doc", "quote"} {
			if !compiled.Syntax[id].Italic {
				t.Errorf("%s: syntax %s lost its italic", compiled.Name, id)
			}
		}
		if !compiled.Syntax["heading"].Bold {
			t.Errorf("%s: headings lost their weight", compiled.Name)
		}
	}
}

// loadStockForTest compiles the stock theme units.
func loadStockForTest(t *testing.T) []Compiled {
	t.Helper()
	dir := filepath.Join("..", "..", "config", "packs", "painted-wolf",
		"platform", "contributions", "themes")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read stock themes: %v", err)
	}
	var out []Compiled
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		var raw struct {
			ID         string               `yaml:"id"`
			Name       string               `yaml:"name"`
			Appearance string               `yaml:"appearance"`
			Tokens     map[string]string    `yaml:"tokens"`
			Syntax     map[string]yaml.Node `yaml:"syntax"`
		}
		if err := yaml.Unmarshal(body, &raw); err != nil {
			t.Fatalf("decode %s: %v", entry.Name(), err)
		}
		decl := Declaration{
			ID: raw.ID, Name: raw.Name,
			Appearance: Appearance(raw.Appearance), Tokens: raw.Tokens,
			Syntax: map[string]SyntaxDeclaration{},
		}
		for id, node := range raw.Syntax {
			var entry SyntaxDeclaration
			if node.Kind == yaml.ScalarNode {
				if err := node.Decode(&entry.Color); err != nil {
					t.Fatalf("decode syntax %s: %v", id, err)
				}
			} else {
				var obj struct {
					Color     string `yaml:"color"`
					Italic    bool   `yaml:"italic"`
					Bold      bool   `yaml:"bold"`
					Underline bool   `yaml:"underline"`
				}
				if err := node.Decode(&obj); err != nil {
					t.Fatalf("decode syntax %s: %v", id, err)
				}
				entry = SyntaxDeclaration{Color: obj.Color, Italic: obj.Italic,
					Bold: obj.Bold, Underline: obj.Underline}
			}
			decl.Syntax[id] = entry
		}
		compiled, err := Compile(decl)
		if err != nil {
			t.Fatalf("compile %s: %v", entry.Name(), err)
		}
		out = append(out, compiled)
	}
	if len(out) != 2 {
		t.Fatalf("stock themes = %d, want the shipped light and dark pair", len(out))
	}
	return out
}
