package designkit

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// Font describes one bundled OFL family.
type Font struct {
	Family string
	File   string // basename under fonts/
}

// fonts is the curated hermetic type catalog (variable OFL WOFF2).
var fonts = []Font{
	{Family: "Inter", File: "Inter.woff2"},
	{Family: "Source Serif 4", File: "SourceSerif4.woff2"},
	{Family: "Source Sans 3", File: "SourceSans3.woff2"},
	{Family: "JetBrains Mono", File: "JetBrainsMono.woff2"},
	{Family: "Fraunces", File: "Fraunces.woff2"},
	{Family: "Space Grotesk", File: "SpaceGrotesk.woff2"},
	{Family: "DM Sans", File: "DMSans.woff2"},
	{Family: "Playfair Display", File: "PlayfairDisplay.woff2"},
	{Family: "IBM Plex Sans", File: "IBMPlexSans.woff2"},
	{Family: "Literata", File: "Literata.woff2"},
}

var fontByFamily = func() map[string]Font {
	m := make(map[string]Font, len(fonts))
	for _, f := range fonts {
		m[strings.ToLower(f.Family)] = f
	}
	return m
}()

// Fonts returns a copy of the hermetic type catalog.
func Fonts() []Font {
	return append([]Font(nil), fonts...)
}

// FontFamilies returns catalog family names in stable order.
func FontFamilies() []string {
	out := make([]string, len(fonts))
	for i, f := range fonts {
		out[i] = f.Family
	}
	return out
}

// LookupFont resolves a catalog family (case-insensitive).
func LookupFont(family string) (Font, bool) {
	f, ok := fontByFamily[strings.ToLower(strings.TrimSpace(family))]
	return f, ok
}

// ValidateFonts rejects unknown catalog names.
func ValidateFonts(names []string) error {
	var unknown []string
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if _, ok := LookupFont(n); !ok {
			unknown = append(unknown, n)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	return fmt.Errorf("unknown fonts: %s", strings.Join(unknown, ", "))
}

// FontFile returns the raw WOFF2 for a catalog basename.
func FontFile(name string) ([]byte, error) {
	data, err := readEmbedded("fonts/" + name)
	if err != nil {
		return nil, fmt.Errorf("read font %s: %w", name, err)
	}
	return data, nil
}

// LicenseFile returns license text by its provenance-relative path.
func LicenseFile(path string) ([]byte, error) {
	if !strings.HasPrefix(path, "licenses/") {
		return nil, fmt.Errorf("licence path %q is outside licenses/", path)
	}
	data, err := readEmbedded(path)
	if err != nil {
		return nil, fmt.Errorf("read licence %s: %w", path, err)
	}
	return data, nil
}

// FontSrc selects how @font-face src URLs are emitted.
type FontSrc int

const (
	// FontSrcInline embeds WOFF2 as a data: URL.
	FontSrcInline FontSrc = iota
	// FontSrcKitOrigin points at bundled capture fonts.
	FontSrcKitOrigin
)

// FontFaceRule emits one @font-face for a catalog font.
func FontFaceRule(f Font, src FontSrc, display string) string {
	display = strings.TrimSpace(display)
	if display == "" {
		display = "block"
	}
	var srcURL string
	switch src {
	case FontSrcKitOrigin:
		srcURL = fmt.Sprintf("%sfonts/%s", KitOrigin, f.File)
	default:
		data, err := readEmbedded("fonts/" + f.File)
		if err != nil {
			panic(fmt.Sprintf("designkit font %s: %v", f.File, err))
		}
		srcURL = "data:font/woff2;base64," + base64.StdEncoding.EncodeToString(data)
	}
	return fmt.Sprintf(
		`@font-face{font-family:%q;font-style:normal;font-weight:100 900;font-display:%s;src:url(%q) format("woff2");}`,
		f.Family, display, srcURL,
	)
}

// FontFaceCSS emits inlined @font-face rules for the full catalog (render_view).
func FontFaceCSS() string {
	var b strings.Builder
	for _, f := range fonts {
		b.WriteString(FontFaceRule(f, FontSrcInline, "block"))
		b.WriteByte('\n')
	}
	return b.String()
}
