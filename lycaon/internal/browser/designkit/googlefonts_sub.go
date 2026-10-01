package designkit

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

)

const (
	googleFontsCSSHost    = "fonts.googleapis.com"
	googleFontsStaticHost = "fonts.gstatic.com"
)

// FontResponse is a substitute answer for a remote-font request. Blocked answers fail the
// request the way a client-side block does.
type FontResponse struct {
	Status  int
	Headers map[string]string
	Body    []byte
	Blocked bool
}

// FontSubstitute answers supported remote-font requests from bundled files.
// It reports whether the request is one the kit answers.
func FontSubstitute(reqURL *url.URL) (FontResponse, bool) {
	if reqURL == nil {
		return FontResponse{}, false
	}
	host := strings.ToLower(reqURL.Hostname())
	p := path.Clean("/" + reqURL.EscapedPath())
	switch host {
	case googleFontsCSSHost:
		return googleFontsCSS(reqURL, p), true
	case googleFontsStaticHost:
		return gstaticFont(p), true
	default:
		if sameOrigin(KitOrigin, reqURL.String()) && strings.HasPrefix(p, "/fonts/") {
			return kitFontFile(path.Base(p)), true
		}
		return FontResponse{}, false
	}
}

func googleFontsCSS(reqURL *url.URL, p string) FontResponse {
	if p != "/css" && p != "/css2" {
		return FontResponse{Status: http.StatusNoContent}
	}
	css := GoogleFontsSubstituteCSS(ParseGoogleFontFamilies(reqURL))
	return FontResponse{
		Status:  http.StatusOK,
		Headers: map[string]string{"Content-Type": "text/css; charset=utf-8", "Access-Control-Allow-Origin": "*"},
		Body:    []byte(css),
	}
}

func gstaticFont(p string) FontResponse {
	slug := gstaticFamilySlug(p)
	if slug == "" {
		return FontResponse{Blocked: true}
	}
	font, ok := LookupFontByCompactName(slug)
	if !ok {
		return FontResponse{Blocked: true}
	}
	return kitFontFile(font.File)
}

func kitFontFile(name string) FontResponse {
	name = path.Base(strings.TrimSpace(name))
	notFound := FontResponse{Status: http.StatusNotFound, Body: []byte("not found")}
	if name == "." || name == "/" || name == ".." {
		return notFound
	}
	data, err := readEmbedded("fonts/" + name)
	if err != nil {
		return notFound
	}
	return FontResponse{
		Status:  http.StatusOK,
		Headers: map[string]string{"Content-Type": "font/woff2", "Access-Control-Allow-Origin": "*"},
		Body:    data,
	}
}

// ParseGoogleFontFamilies extracts family names from a fonts.googleapis.com CSS URL.
func ParseGoogleFontFamilies(u *url.URL) []string {
	if u == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(raw string) {
		family := googleFamilyToken(raw)
		if family == "" {
			return
		}
		key := strings.ToLower(family)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, family)
	}
	// In the CSS API, ';' separates weights rather than query parameters. url.Values
	// treats ';' as '&', so parse RawQuery manually.
	for _, part := range strings.Split(u.RawQuery, "&") {
		key, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		if key != "family" {
			continue
		}
		decoded, err := url.QueryUnescape(val)
		if err != nil {
			decoded = val
		}
		// The css v1 endpoint accepts pipe-separated families.
		if strings.Contains(decoded, "|") {
			for _, piece := range strings.Split(decoded, "|") {
				add(piece)
			}
			continue
		}
		add(decoded)
	}
	return out
}

func googleFamilyToken(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Variant selectors follow the family name after a colon.
	if i := strings.IndexByte(raw, ':'); i >= 0 {
		raw = raw[:i]
	}
	raw = strings.ReplaceAll(raw, "+", " ")
	return strings.TrimSpace(raw)
}

// GoogleFontsSubstituteCSS points known families at bundled WOFF2 files.
// Kit-origin URLs let capture fulfill font requests without inlining bytes.
func GoogleFontsSubstituteCSS(families []string) string {
	var b strings.Builder
	b.WriteString("/* Bundled OFL fonts served from the kit origin. */\n")
	for _, name := range families {
		f, ok := LookupFont(name)
		if !ok {
			fmt.Fprintf(&b, "/* skipped unknown family %q */\n", name)
			continue
		}
		b.WriteString(FontFaceRule(f, FontSrcKitOrigin, "swap"))
		b.WriteByte('\n')
	}
	return b.String()
}

func gstaticFamilySlug(p string) string {
	// Family slugs follow /s/ in asset paths.
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "s" {
			return strings.ToLower(parts[i+1])
		}
	}
	return ""
}

// LookupFontByCompactName resolves "inter" / "jetbrainsmono" style slugs.
func LookupFontByCompactName(compact string) (Font, bool) {
	compact = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(compact), "-", ""))
	compact = strings.ReplaceAll(compact, " ", "")
	if compact == "" {
		return Font{}, false
	}
	for _, f := range fonts {
		key := strings.ToLower(strings.ReplaceAll(f.Family, " ", ""))
		if key == compact {
			return f, true
		}
		fileKey := strings.ToLower(strings.TrimSuffix(f.File, ".woff2"))
		fileKey = strings.ReplaceAll(fileKey, "-", "")
		if fileKey == compact {
			return f, true
		}
	}
	return Font{}, false
}
