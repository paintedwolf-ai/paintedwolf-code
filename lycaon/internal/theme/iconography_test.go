package theme

import (
	"strings"
	"testing"
)

// Plain child geometry compiles directly.
func TestParseIconGeometryAcceptsPlainShapes(t *testing.T) {
	t.Parallel()

	nodes, err := parseIconGeometry(
		`<circle cx="7" cy="7" r="4.25" /><path d="M13.5 13.5 10.1 10.1" />`)
	if err != nil {
		t.Fatalf("parseIconGeometry: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(nodes))
	}
	if nodes[0].Tag != "circle" || nodes[0].Attrs["r"] != "4.25" {
		t.Errorf("first node = %+v", nodes[0])
	}
	if nodes[1].Tag != "path" || !strings.HasPrefix(nodes[1].Attrs["d"], "M13.5") {
		t.Errorf("second node = %+v", nodes[1])
	}
}

// Glyph paint is inherited or absent.
func TestParseIconGeometryRefusesItsOwnColor(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{
		`<path d="M1 1" fill="#ff0000"/>`,
		`<path d="M1 1" stroke="rgb(255,0,0)"/>`,
		`<path d="M1 1" fill="red"/>`,
		`<path d="M1 1" fill="url(#grad)"/>`,
	} {
		t.Run(bad, func(t *testing.T) {
			t.Parallel()
			if _, err := parseIconGeometry(bad); err == nil {
				t.Errorf("accepted a glyph carrying its own color: %s", bad)
			}
		})
	}
	for _, ok := range []string{
		`<path d="M1 1L2 2" fill="currentColor"/>`,
		`<path d="M1 1L2 2" fill="none" stroke="currentColor"/>`,
	} {
		if _, err := parseIconGeometry(ok); err != nil {
			t.Errorf("rejected %s: %v", ok, err)
		}
	}
}

func TestParseIconGeometryRefusesEverythingThatIsNotAShape(t *testing.T) {
	t.Parallel()

	for name, markup := range map[string]string{
		"script":         `<script>alert(1)</script>`,
		"script in g":    `<g><script>alert(1)</script></g>`,
		"foreign object": `<foreignObject><div>hi</div></foreignObject>`,
		"external image": `<image href="https://example.test/x.png"/>`,
		"xlink href":     `<use xlink:href="#other"/>`,
		"inline style":   `<path d="M1 1" style="fill:red"/>`,
		"event handler":  `<path d="M1 1" onload="alert(1)"/>`,
		"animation":      `<animate attributeName="fill" to="red"/>`,
		"text":           `<text x="1" y="1">hi</text>`,
		"text content":   `<g>hello</g>`,
		"svg wrapper":    `<svg><path d="M1 1L2 2"/></svg>`,
		"id for url ref": `<path id="grad" d="M1 1"/>`,
		"clip path ref":  `<path d="M1 1" clip-path="url(#c)"/>`,
		"malformed":      `<path d="M1 1"`,
		"empty":          ``,
		"comment":        `<!-- hi --><path d="M1 1"/>`,
		"doctype":        `<!DOCTYPE svg><path d="M1 1"/>`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseIconGeometry(markup); err == nil {
				t.Errorf("accepted %s: %s", name, markup)
			}
		})
	}
}

func TestParseIconGeometryRejectsShapesThatDrawNothing(t *testing.T) {
	t.Parallel()

	for name, markup := range map[string]string{
		"empty path":                `<path d=""/>`,
		"moveto only":               `<path d="M1 1"/>`,
		"zero-length line path":     `<path d="M1 2L1 2"/>`,
		"zero-length relative path": `<path d="M1 2l0 0"/>`,
		"zero-area curve path":      `<path d="M1 2C1 2 1 2 1 2"/>`,
		"zero-length close path":    `<path d="M1 2Z"/>`,
		"moveto sequence only":      `<path d="M1 2M3 4"/>`,
		"bad path command":          `<path d="M1 1R2 2"/>`,
		"wrong path arity":          `<path d="M1 1L2"/>`,
		"bad arc flag":              `<path d="M1 1A2 2 0 4 0 3 3"/>`,
		"non-finite coordinate":     `<circle cx="1e999" cy="2" r="1"/>`,
		"non-number radius":         `<circle r="banana"/>`,
		"missing radius":            `<circle cx="8" cy="8"/>`,
		"zero radius":               `<circle cx="8" cy="8" r="0"/>`,
		"rect without dimensions":   `<rect x="1" y="1"/>`,
		"ellipse without radii":     `<ellipse cx="8" cy="8"/>`,
		"zero-length line":          `<line x1="1" y1="1" x2="1" y2="1"/>`,
		"one polyline point":        `<polyline points="1 1"/>`,
		"two polygon points":        `<polygon points="1 1 2 2"/>`,
		"odd point coordinate":      `<polyline points="1 1 2"/>`,
		"empty group":               `<g/>`,
		"attribute on wrong shape":  `<circle d="M1 1L2 2" r="1"/>`,
		"invisible opacity":         `<circle r="1" opacity="0"/>`,
		"no paint":                  `<circle r="1" fill="none" stroke="none"/>`,
		"invalid stroke width":      `<path d="M1 1L2 2" stroke-width="wide"/>`,
		"zero stroke width":         `<path d="M1 1L2 2" stroke-width="0"/>`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseIconGeometry(markup); err == nil {
				t.Errorf("accepted non-drawing geometry: %s", markup)
			}
		})
	}
}

func TestParseIconGeometryAcceptsTypedShapeGeometry(t *testing.T) {
	t.Parallel()

	markup := `<rect x="1" y="1" width="3" height="4" rx=".5"/>` +
		`<ellipse cx="8" cy="8" rx="2" ry="3"/>` +
		`<line x1="0" y1="0" x2="1e1" y2="10"/>` +
		`<polyline points="0,0 2,2 4,0"/>` +
		`<polygon points="8,1 14,14 2,14" fill-rule="evenodd"/>`
	if _, err := parseIconGeometry(markup); err != nil {
		t.Fatalf("parseIconGeometry: %v", err)
	}
}

func TestParseIconGeometryAcceptsTheSupportedSVGPathGrammar(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"M0 0L1 1",
		"m1 1l1 0",
		"M0 0H1V2",
		"M0 0C1 0 1 1 2 1 3 1 3 2 4 2",
		"M0 0C1 0 1 1 2 1S3 2 4 2",
		"M0 0Q1 1 2 0 3 -1 4 0",
		"M0 0Q1 1 2 0T4 0",
		"M0 0A2 2 0 1 0 4 0",
		"M0 0L1 0L0 1Z",
		"M0 0 1 1 2 0",
	} {
		if _, err := parseIconGeometry(`<path d="` + path + `"/>`); err != nil {
			t.Errorf("rejected path %q: %v", path, err)
		}
	}
}

// Accept transforms commonly emitted with grouped geometry.
func TestParseIconGeometryAcceptsTransforms(t *testing.T) {
	t.Parallel()

	for _, ok := range []struct{ markup, want string }{
		{`<g transform="translate(4 4)"><path d="M0 0L8 8"/></g>`, "translate(4 4)"},
		{`<g transform="translate(4,4)"><path d="M0 0L1 1"/></g>`, "translate(4 4)"},
		{`<g transform="rotate(45 8 8)"><path d="M0 0L1 1"/></g>`, "rotate(45 8 8)"},
		{`<g transform="scale(-1 1)"><path d="M0 0L1 1"/></g>`, "scale(-1 1)"},
		{`<g transform="matrix(1 0 0 1 2 3)"><path d="M0 0L1 1"/></g>`, "matrix(1 0 0 1 2 3)"},
		{`<g transform="skewX(1.5e1)"><path d="M0 0L1 1"/></g>`, "skewX(15)"},
		{`<path d="M0 0L1 1" transform="translate(2) rotate(90)"/>`, "translate(2) rotate(90)"},
	} {
		t.Run(ok.markup, func(t *testing.T) {
			t.Parallel()
			nodes, err := parseIconGeometry(ok.markup)
			if err != nil {
				t.Fatalf("rejected a well-formed transform: %v", err)
			}
			if got := nodes[0].Attrs["transform"]; got != ok.want {
				t.Errorf("transform = %q, want %q", got, ok.want)
			}
		})
	}
}

// Transforms accept only the closed grammar.
func TestParseIconGeometryRefusesBadTransforms(t *testing.T) {
	t.Parallel()

	for name, markup := range map[string]string{
		"url reference":    `<path d="M0 0L1 1" transform="url(#x)"/>`,
		"unknown function": `<path d="M0 0L1 1" transform="translateZ(4)"/>`,
		"nested call":      `<path d="M0 0L1 1" transform="translate(scale(2))"/>`,
		"non-numeric arg":  `<path d="M0 0L1 1" transform="translate(4px)"/>`,
		"non-finite arg":   `<path d="M0 0L1 1" transform="translate(1e999)"/>`,
		"leading comma":    `<path d="M0 0L1 1" transform="translate(,4)"/>`,
		"trailing comma":   `<path d="M0 0L1 1" transform="translate(4,)"/>`,
		"repeated comma":   `<path d="M0 0L1 1" transform="translate(4,,5)"/>`,
		"too many args":    `<path d="M0 0L1 1" transform="rotate(1 2)"/>`,
		"too few args":     `<path d="M0 0L1 1" transform="matrix(1 0 0 1)"/>`,
		"no call at all":   `<path d="M0 0L1 1" transform="4 4"/>`,
		"empty":            `<path d="M0 0L1 1" transform=""/>`,
		"trailing junk":    `<path d="M0 0L1 1" transform="translate(4) alert"/>`,
		"unbalanced":       `<path d="M0 0L1 1" transform="translate(4"/>`,
		"over the cap":     `<path d="M0 0L1 1" transform="` + strings.Repeat("rotate(1) ", 9) + `"/>`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseIconGeometry(markup); err == nil {
				t.Errorf("accepted %s: %s", name, markup)
			}
		})
	}
}

// Glyph payloads stay within fixed bounds.
func TestParseIconGeometryBoundsSize(t *testing.T) {
	t.Parallel()

	if _, err := parseIconGeometry(strings.Repeat(`<path d="M1 1L2 2"/>`, 1024)); err == nil {
		t.Error("accepted a glyph over the byte limit")
	}
	if _, err := parseIconGeometry(strings.Repeat(`<path d="M1 1L2 2"/>`, 65)); err == nil {
		t.Error("accepted a glyph over the shape limit")
	}
	deep := strings.Repeat("<g>", 6) + `<path d="M1 1"/>` + strings.Repeat("</g>", 6)
	if _, err := parseIconGeometry(deep); err == nil {
		t.Error("accepted a glyph nested past the depth limit")
	}
}

// Icon slot ids use the closed vocabulary.
func TestCompileRejectsAnUnknownIconSlot(t *testing.T) {
	t.Parallel()

	_, err := Compile(Declaration{
		Name: "X", Appearance: AppearanceLight, Tokens: minimalTokens(),
		Icons: map[string]string{"nonesuch": `<path d="M1 1"/>`},
	})
	if err == nil {
		t.Fatal("Compile accepted an unknown icon slot")
	}
	if !strings.Contains(err.Error(), "unknown icon slot") {
		t.Errorf("error = %v", err)
	}
}

// Compiled icons contain only authored overrides.
func TestCompiledIconsCarryOnlyWhatWasAuthored(t *testing.T) {
	t.Parallel()

	compiled, err := Compile(Declaration{
		Name: "X", Appearance: AppearanceLight, Tokens: minimalTokens(),
		Icons: map[string]string{"check": `<path d="M3 8.5 6.5 12 13 4.5"/>`},
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(compiled.Icons) != 1 {
		t.Fatalf("icons = %+v, want only the authored slot", compiled.Icons)
	}
	if _, ok := compiled.Icons["check"]; !ok {
		t.Error("the authored slot is missing")
	}
}
