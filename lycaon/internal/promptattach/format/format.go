// Package format detects attachment formats from leading bytes.
package format

import (
	"bytes"
	"net/http"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/promptattach/docformat"
)

// Kind is the family a detected format belongs to.
type Kind string

const (
	// KindImage is a raster the Visual plane can accept.
	KindImage Kind = "image"
	// KindText is UTF-8 text-family source or data.
	KindText Kind = "text"
	// KindDocument is a package or document the extractor can open.
	KindDocument Kind = "document"
	// KindVideo is a video container the managed browser may decode.
	KindVideo Kind = "video"
	// KindContainer wraps one inner body and must be unwrapped before routing.
	KindContainer Kind = "container"
	// KindUnsupported is everything else.
	KindUnsupported Kind = "unsupported"
)

// Format is one detection result.
type Format struct {
	Kind Kind
	// MIME is the normalized type for a terminal format, empty for containers.
	MIME string
	// Container names the decoder when Kind is KindContainer.
	Container Container
}

// Terminal reports whether the format carries content directly.
func (f Format) Terminal() bool { return f.Kind != KindContainer && f.Kind != KindUnsupported }

// PeekBytes bounds the detection sample.
const PeekBytes = 8 << 10

// Detect classifies a bounded sample.
func Detect(filename, reportedMIME string, peek []byte) Format {
	reported := normalizeMIME(reportedMIME)
	if reported == "text/uri-list" || reported == "message/external-body" {
		return Format{Kind: KindUnsupported, MIME: reported}
	}

	// Container signatures take precedence over text probing.
	if c, ok := detectContainer(peek); ok {
		return Format{Kind: KindContainer, Container: c}
	}
	if f, ok := detectDocument(filename, reported, peek); ok {
		return f
	}
	if mime, ok := detectVideo(peek); ok {
		return Format{Kind: KindVideo, MIME: mime}
	}
	if f, ok := detectImage(reported, filename, peek); ok {
		return f
	}
	if f, ok := detectText(filename, reported, peek); ok {
		return f
	}
	return Format{Kind: KindUnsupported, MIME: firstNonEmpty(reported, sniffMIME(peek), "application/octet-stream")}
}

func detectDocument(filename, reported string, peek []byte) (Format, bool) {
	f, ok := docformat.Detect(filename, reported, peek)
	if !ok {
		return Format{}, false
	}
	// Document hints require matching signature bytes.
	var valid bool
	switch f {
	case docformat.PDF:
		valid = bytes.HasPrefix(peek, []byte("%PDF-"))
	case docformat.RTF:
		valid = bytes.HasPrefix(bytes.TrimSpace(peek), []byte(`{\rtf`))
	default:
		valid = bytes.HasPrefix(peek, []byte{'P', 'K', 0x03, 0x04}) ||
			bytes.HasPrefix(peek, []byte{'P', 'K', 0x05, 0x06})
	}
	if valid {
		return Format{Kind: KindDocument, MIME: documentMIME(f, reported)}, true
	}
	return Format{Kind: KindUnsupported, MIME: documentMIME(f, reported)}, true
}

// detectVideo recognizes MP4 and QuickTime by their ftyp box and WebM by its EBML header.
// Whether the codec inside decodes is the browser's answer, not the container's.
func detectVideo(peek []byte) (string, bool) {
	if len(peek) >= 12 && string(peek[4:8]) == "ftyp" {
		brand := string(peek[8:12])
		if _, still := nonVideoBrands[brand]; still {
			return "", false
		}
		if brand == "qt  " {
			return "video/quicktime", true
		}
		return "video/mp4", true
	}
	// QuickTime files written before ftyp open straight onto a top-level atom.
	if len(peek) >= 8 && quickTimeLeadAtoms[string(peek[4:8])] {
		return "video/quicktime", true
	}
	if bytes.HasPrefix(peek, []byte{0x1A, 0x45, 0xDF, 0xA3}) && bytes.Contains(peek[:min(len(peek), 64)], []byte("webm")) {
		return "video/webm", true
	}
	return "", false
}

func detectImage(reported, filename string, peek []byte) (Format, bool) {
	active := LooksLikeActiveMarkup(peek)
	sniffed := sniffMIME(peek)
	if IsRasterMIME(sniffed) && !active {
		return Format{Kind: KindImage, MIME: sniffed}, true
	}
	if !active && !IsRasterMIME(reported) && reported != "image/svg+xml" && !strings.HasPrefix(reported, "image/") {
		return Format{}, false
	}
	// Active markup never reaches the raster path.
	if extHint(filename) == ".svg" && utf8Text(peek) {
		return Format{Kind: KindText, MIME: "image/svg+xml"}, true
	}
	return Format{Kind: KindUnsupported, MIME: firstNonEmpty(reported, "application/octet-stream")}, true
}

func detectText(filename, reported string, peek []byte) (Format, bool) {
	if !utf8Text(peek) {
		return Format{}, false
	}
	sniffed := sniffMIME(peek)
	if _, ok := textFamilyMIME[reported]; ok {
		return Format{Kind: KindText, MIME: reported}, true
	}
	// Suffixes refine ambiguous text-family content.
	if mime, ok := textFamilyExtMIME[extHint(filename)]; ok {
		return Format{Kind: KindText, MIME: mime}, true
	}
	if _, ok := textFamilyMIME[sniffed]; ok {
		return Format{Kind: KindText, MIME: sniffed}, true
	}
	if reported == "text/plain" {
		return Format{Kind: KindText, MIME: "text/plain"}, true
	}
	if hasTextExtHint(filename) {
		return Format{Kind: KindText, MIME: firstNonEmpty(reported, sniffed, "text/plain")}, true
	}
	return Format{}, false
}

func documentMIME(f docformat.Format, reported string) string {
	if reported != "" && isDocumentMIME(reported) {
		return reported
	}
	return docformat.MIME(f)
}

func isDocumentMIME(mime string) bool {
	switch mime {
	case "application/pdf", "application/rtf", "text/rtf":
		return true
	}
	return strings.Contains(mime, "officedocument") || strings.Contains(mime, "opendocument")
}

func normalizeMIME(mime string) string {
	return strings.ToLower(strings.TrimSpace(mime))
}

func sniffMIME(peek []byte) string {
	sniffed := http.DetectContentType(peek)
	if i := strings.IndexByte(sniffed, ';'); i >= 0 {
		sniffed = sniffed[:i]
	}
	return strings.ToLower(strings.TrimSpace(sniffed))
}

// utf8Text allows a trailing partial rune in the sample.
func utf8Text(peek []byte) bool {
	if len(peek) == 0 || bytes.IndexByte(peek, 0) >= 0 {
		return false
	}
	trimmed := peek
	for len(trimmed) > 0 && !utf8.Valid(trimmed) && len(peek)-len(trimmed) < utf8.UTFMax {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return utf8.Valid(trimmed)
}

// LooksLikeActiveMarkup detects active markup prefixes.
func LooksLikeActiveMarkup(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	lower := strings.ToLower(string(trimmed[:min(len(trimmed), 64)]))
	return strings.HasPrefix(lower, "<?xml") ||
		strings.HasPrefix(lower, "<svg") ||
		strings.HasPrefix(lower, "<!doctype html") ||
		strings.HasPrefix(lower, "<html")
}

// IsRasterMIME reports whether mime is in the host's accepted prompt-image set.
func IsRasterMIME(mime string) bool {
	_, ok := rasterMIME[normalizeMIME(mime)]
	return ok
}

// IsStructuredQueryMIME reports whether structured queries are available.
func IsStructuredQueryMIME(mime string) bool {
	_, ok := structuredQueryMIME[normalizeMIME(mime)]
	return ok
}

// hasTextExtHint detects a text-family filename hint.
func hasTextExtHint(filename string) bool {
	base := strings.ToLower(strings.TrimSpace(path.Base(filename)))
	if base == "" {
		return false
	}
	if _, ok := textFamilyBasename[base]; ok {
		return true
	}
	if strings.HasSuffix(base, ".env.example") {
		return true
	}
	ext := path.Ext(base)
	if ext == "" {
		return false
	}
	_, ok := textFamilyExt[ext]
	return ok
}

func extHint(filename string) string {
	base := strings.ToLower(strings.TrimSpace(path.Base(filename)))
	if strings.HasSuffix(base, ".env.example") {
		return ".env.example"
	}
	return path.Ext(base)
}

// RasterMIMEs returns the host-defined accepted prompt-image MIME set.
func RasterMIMEs() []string { return sortedKeys(rasterMIME) }

// VideoMIMEs returns the video container MIME set intake accepts.
func VideoMIMEs() []string { return sortedKeys(videoMIME) }

// IsVideoMIME reports whether mime is an accepted video container.
func IsVideoMIME(mime string) bool {
	_, ok := videoMIME[normalizeMIME(mime)]
	return ok
}

// TextFamilyMIMEs returns the host-defined text-family MIME set.
func TextFamilyMIMEs() []string { return sortedKeys(textFamilyMIME) }

// TextFamilyExtensions returns the filename extension hints used by intake.
func TextFamilyExtensions() []string { return sortedKeys(textFamilyExt) }

// TextFamilyBasenames returns the extensionless filename hints used by intake.
func TextFamilyBasenames() []string { return sortedKeys(textFamilyBasename) }

func sortedKeys(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" && v != "application/octet-stream" {
			return v
		}
	}
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return "application/octet-stream"
}
