package webresearch

import (
	"bytes"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"
)

// periodCurrent is the wire spelling of the default window.
const periodCurrent = "current"

// Declared windows are bounded: below the floor a four-digit number is a
// subject name ("Windows 2000"), not a research window; above the ceiling it is
// a typo the pipeline cannot rank for.
const (
	periodFloorYear   = 1990
	periodCeilingYear = 2999
)

// Period is the time window a search is about — the `period` argument of
// web_search, and the only control signal for time. A year inside the query
// string is ordinary text that steers nothing, which keeps subject years
// ("CVE-2025-1234") searchable. The zero value is the current window.
type Period struct {
	// from and to are inclusive years. Both zero means the current window.
	from int
	to   int
}

// CurrentPeriod is the default window: today's state, recency ranked.
func CurrentPeriod() Period { return Period{} }

// ParsePeriod reads the wire value: "", "current", "YYYY", or "YYYY-YYYY".
func ParsePeriod(raw string) (Period, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" || s == periodCurrent {
		return Period{}, nil
	}
	lo, hi, ranged := strings.Cut(s, "-")
	from, err := parsePeriodYear(lo)
	if err != nil {
		return Period{}, err
	}
	to := from
	if ranged {
		if to, err = parsePeriodYear(hi); err != nil {
			return Period{}, err
		}
	}
	if to < from {
		return Period{}, fmt.Errorf("period %q ends before it starts", raw)
	}
	return Period{from: from, to: to}, nil
}

func parsePeriodYear(raw string) (int, error) {
	s := strings.TrimSpace(raw)
	if len(s) != 4 {
		return 0, fmt.Errorf("period year %q is not four digits", raw)
	}
	year, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("period year %q is not a number", raw)
	}
	if year < periodFloorYear || year > periodCeilingYear {
		return 0, fmt.Errorf("period year %q is outside %d-%d", raw, periodFloorYear, periodCeilingYear)
	}
	return year, nil
}

// IsCurrent reports the default window — no year was declared.
func (p Period) IsCurrent() bool { return p.from == 0 }

// Historical reports a declared window that closed before the current year.
// The pages such a search wants are, by construction, the older ones.
func (p Period) Historical(now time.Time) bool {
	return !p.IsCurrent() && p.to < now.Year()
}

// Includes reports whether a page's year falls inside the declared window.
// The current window includes nothing: it ranks by recency, not by year match.
func (p Period) Includes(year int) bool {
	if p.IsCurrent() {
		return false
	}
	return year >= p.from && year <= p.to
}

// Newest is the latest year in the window, or 0 for the current window.
func (p Period) Newest() int {
	if p.IsCurrent() {
		return 0
	}
	return p.to
}

// String is the wire spelling, round-tripping through ParsePeriod.
func (p Period) String() string {
	if p.IsCurrent() {
		return periodCurrent
	}
	if p.from == p.to {
		return strconv.Itoa(p.from)
	}
	return strconv.Itoa(p.from) + "-" + strconv.Itoa(p.to)
}

// years lists the window's years, oldest first. Empty for the current window.
func (p Period) years() []string {
	if p.IsCurrent() {
		return nil
	}
	out := make([]string, 0, p.to-p.from+1)
	for y := p.from; y <= p.to; y++ {
		out = append(out, strconv.Itoa(y))
	}
	return out
}

// bareYearToken returns the first standalone past-year token in a query, or "".
// Standalone means the whole token is the year, so subject years inside a larger
// token ("CVE-2025-1234") do not match. Drives teaching copy only — nothing in
// ranking reads it.
func bareYearToken(query string, now time.Time) string {
	for _, field := range strings.Fields(query) {
		tok := strings.Trim(field, `"'.,:;!?()[]{}`)
		if len(tok) != 4 {
			continue
		}
		year, err := strconv.Atoi(tok)
		if err != nil {
			continue
		}
		if year >= periodFloorYear && year < now.Year() {
			return tok
		}
	}
	return ""
}

// providerQuery is the string handed to catalog providers. They take a query
// and nothing else, so a declared window has to ride in the text.
func (p Period) providerQuery(query string) string {
	if p.IsCurrent() {
		return query
	}
	return strings.TrimSpace(query + " " + strings.Join(p.years(), " "))
}

// mimeClass routes mode=raw bodies: textual stays in agent context; binary requires dest.
type mimeClass string

const (
	mimeTextual     mimeClass = "textual"
	mimeBinary      mimeClass = "binary"
	mimeUnsupported mimeClass = "unsupported"
)

var binaryExt = map[string]struct{}{
	".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".webp": {}, ".ico": {},
	".woff": {}, ".woff2": {}, ".ttf": {}, ".otf": {}, ".eot": {},
}

var textualExt = map[string]struct{}{
	".html": {}, ".htm": {}, ".css": {}, ".js": {}, ".mjs": {}, ".cjs": {},
	".json": {}, ".svg": {}, ".xml": {}, ".txt": {}, ".md": {}, ".markdown": {},
	".yaml": {}, ".yml": {}, ".ts": {}, ".tsx": {}, ".jsx": {}, ".map": {},
	".csv": {}, ".tsv": {}, ".svgz": {},
}

var unsupportedExt = map[string]struct{}{
	".zip": {}, ".tar": {}, ".gz": {}, ".tgz": {}, ".bz2": {}, ".7z": {}, ".rar": {},
	".mp4": {}, ".webm": {}, ".mov": {}, ".mp3": {}, ".wav": {}, ".ogg": {},
	".pdf": {}, ".wasm": {}, ".exe": {}, ".dll": {}, ".so": {}, ".dmg": {},
}

// classifyFetchMIME decides how mode=raw treats a response body.
func classifyFetchMIME(contentType, urlPath string, body []byte) (mimeClass, string) {
	ct := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	ext := strings.ToLower(path.Ext(strings.TrimSpace(urlPath)))

	if _, ok := unsupportedExt[ext]; ok {
		return mimeUnsupported, "extension"
	}
	switch {
	case ct == "application/zip", ct == "application/x-tar",
		ct == "application/gzip", ct == "application/x-gzip",
		strings.HasPrefix(ct, "video/"), strings.HasPrefix(ct, "audio/"),
		ct == "application/pdf", ct == "application/wasm":
		return mimeUnsupported, "content_type"
	}

	if ct == "image/svg+xml" || ext == ".svg" {
		return mimeTextual, "svg"
	}
	if strings.HasPrefix(ct, "text/") ||
		ct == "application/json" ||
		ct == "application/ld+json" ||
		ct == "application/javascript" ||
		ct == "application/x-javascript" ||
		ct == "application/ecmascript" ||
		ct == "application/xml" ||
		ct == "application/xhtml+xml" ||
		ct == "application/yaml" ||
		ct == "application/x-yaml" {
		return mimeTextual, "content_type"
	}
	if _, ok := textualExt[ext]; ok && (ct == "" || ct == "application/octet-stream") {
		return mimeTextual, "extension"
	}

	if strings.HasPrefix(ct, "image/") ||
		strings.HasPrefix(ct, "font/") ||
		ct == "application/font-woff" ||
		ct == "application/font-woff2" ||
		ct == "application/vnd.ms-fontobject" {
		return mimeBinary, "content_type"
	}
	if _, ok := binaryExt[ext]; ok {
		return mimeBinary, "extension"
	}

	// Sniff: UTF-8-ish leading bytes that start with '<' look like markup.
	if ct == "" || ct == "application/octet-stream" {
		trim := strings.TrimSpace(string(body))
		if strings.HasPrefix(trim, "<") && looksMostlyText(body) {
			return mimeTextual, "sniff"
		}
		if looksMostlyText(body) && !hasNUL(body) {
			return mimeTextual, "sniff"
		}
		if hasNUL(body) {
			return mimeUnsupported, "binary_sniff"
		}
	}

	if ct == "application/octet-stream" {
		return mimeUnsupported, "octet_stream"
	}
	if ct != "" {
		return mimeUnsupported, "content_type"
	}
	return mimeUnsupported, "unknown"
}

func hasNUL(b []byte) bool {
	return bytes.IndexByte(b, 0) >= 0
}

func looksMostlyText(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	sample := b
	if len(sample) > 512 {
		sample = sample[:512]
	}
	nonPrint := 0
	for _, c := range sample {
		if c == 0 {
			return false
		}
		if c < 0x09 || (c > 0x0d && c < 0x20) {
			nonPrint++
		}
	}
	return nonPrint*10 <= len(sample) // ≤10% control bytes
}

// mediaTypeOnly strips parameters from a Content-Type header value.
func mediaTypeOnly(contentType string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
}

// acceptHeaderForMode picks the Accept header. Raw asks broadly so fonts/images
// are not negotiated away; text stays docs-oriented.
func acceptHeaderForMode(mode string) string {
	if mode == "raw" {
		return "*/*"
	}
	return "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.8"
}
