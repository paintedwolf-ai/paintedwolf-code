package webresearch

import (
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/webindex"
)

// sanitizeHits normalizes provider-authored result text (markup, entities,
// invisible runes, whitespace) at the fan-in, so catalog providers inherit it.
// Provider ids are host vocabulary and are left alone.
func sanitizeHits(hits []WebHit) []WebHit {
	if len(hits) == 0 {
		return hits
	}
	out := hits[:0]
	for _, h := range hits {
		clean, ok := sanitizeHit(h)
		if !ok {
			continue
		}
		out = append(out, clean)
	}
	return out
}

// sanitizeHit normalizes one hit's free text and drops it when the URL is not a
// fetchable web address — there is no correct repair for one.
func sanitizeHit(h WebHit) (WebHit, bool) {
	h.Title = webindex.NormalizeWebTextForStorage(h.Title, 0)
	h.Snippet = webindex.NormalizeWebTextForStorage(h.Snippet, 0)
	h.Date = webindex.NormalizeWebTextForStorage(h.Date, 0)
	h.URL = strings.TrimSpace(h.URL)
	if !webAddress(h.URL) {
		return WebHit{}, false
	}
	if h.Title == "" {
		h.Title = h.URL
	}
	return h, true
}

// webAddress reports an absolute http(s) URL with a host and no control or
// invisible runes. Scheme is compared on the parsed value, so casing and
// surrounding space cannot smuggle one past.
func webAddress(raw string) bool {
	if raw == "" || raw != normalizeControlRunes(raw) {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return false
	}
	return u.Host != ""
}

// normalizeControlRunes strips invisible-format runes, so a URL compared against
// its own normalization reveals them without altering a clean one.
func normalizeControlRunes(raw string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		switch {
		case r >= 0x200b && r <= 0x200f, // zero-width and bidi marks
			r >= 0x202a && r <= 0x202e, // bidi overrides
			r >= 0x2066 && r <= 0x2069, // bidi isolates
			r == 0xfeff:                // BOM / zero-width no-break
			return -1
		}
		return r
	}, raw)
}
