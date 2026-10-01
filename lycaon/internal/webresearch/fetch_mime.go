package webresearch

import (
	"bytes"
	"path"
	"strings"
)

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
