package format

import (
	"sort"
	"strings"
)

// Host-defined format policy.

var rasterMIME = map[string]struct{}{
	"image/png":  {},
	"image/jpeg": {},
	"image/gif":  {},
	"image/webp": {},
}

var videoMIME = map[string]struct{}{
	"video/mp4":       {},
	"video/quicktime": {},
	"video/webm":      {},
}

// nonVideoBrands are ISO base media brands for still images and audio, which share the
// ftyp box with video.
var nonVideoBrands = map[string]struct{}{
	"avif": {}, "avis": {}, "heic": {}, "heix": {}, "heim": {}, "heis": {}, "hevc": {}, "hevx": {},
	"mif1": {}, "msf1": {}, "M4A ": {}, "M4B ": {}, "M4P ": {}, "crx ": {},
}

var quickTimeLeadAtoms = map[string]bool{"moov": true, "mdat": true, "wide": true}

var textFamilyMIME = map[string]struct{}{
	"text/plain":                {},
	"text/markdown":             {},
	"text/csv":                  {},
	"text/tab-separated-values": {},
	"text/html":                 {},
	"text/xml":                  {},
	"application/json":          {},
	"application/yaml":          {},
	"application/x-yaml":        {},
	"application/toml":          {},
	"application/xml":           {},
}

// structuredQueryMIME supports structured attachment queries.
var structuredQueryMIME = map[string]struct{}{
	"application/json":   {},
	"application/yaml":   {},
	"application/x-yaml": {},
	"application/toml":   {},
}

var textFamilyExt = map[string]struct{}{
	".go": {}, ".ts": {}, ".tsx": {}, ".js": {}, ".jsx": {}, ".mjs": {}, ".cjs": {},
	".py": {}, ".rs": {}, ".java": {}, ".kt": {}, ".swift": {},
	".c": {}, ".h": {}, ".cc": {}, ".cpp": {}, ".hpp": {}, ".cs": {},
	".rb": {}, ".php": {}, ".sh": {}, ".bash": {}, ".zsh": {}, ".fish": {}, ".ps1": {},
	".sql": {}, ".graphql": {}, ".proto": {},
	".txt": {}, ".md": {}, ".markdown": {}, ".rst": {},
	".json": {}, ".jsonc": {}, ".yaml": {}, ".yml": {}, ".toml": {},
	".csv": {}, ".tsv": {}, ".log": {},
	".html": {}, ".htm": {}, ".xml": {}, ".css": {}, ".scss": {}, ".less": {},
	".svg": {}, ".env.example": {}, ".gitignore": {}, ".editorconfig": {},
	".dockerfile": {}, ".cmake": {}, ".tf": {}, ".hcl": {},
}

var textFamilyBasename = map[string]struct{}{
	"dockerfile": {},
	"makefile":   {},
}

// textFamilyExtMIME refines already-admitted text.
var textFamilyExtMIME = map[string]string{
	".json":     "application/json",
	".jsonc":    "application/json",
	".yaml":     "application/yaml",
	".yml":      "application/yaml",
	".toml":     "application/toml",
	".xml":      "application/xml",
	".csv":      "text/csv",
	".tsv":      "text/tab-separated-values",
	".md":       "text/markdown",
	".markdown": "text/markdown",
	".html":     "text/html",
	".htm":      "text/html",
}

// WorkspaceImageMIME reports path-format support for the screened workspace viewer.
// It does not admit bytes or expand the stricter prompt-image intake policy.
func WorkspaceImageMIME(extension string) (string, bool) {
	mime, ok := workspaceImageMIME[strings.ToLower(extension)]
	return mime, ok
}

// WorkspaceImageExtensions returns a fresh list of the viewer's supported suffixes.
func WorkspaceImageExtensions() []string {
	out := make([]string, 0, len(workspaceImageMIME))
	for ext := range workspaceImageMIME {
		out = append(out, ext)
	}
	sort.Strings(out)
	return out
}

var workspaceImageMIME = map[string]string{
	".svg":  "image/svg+xml",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
}
