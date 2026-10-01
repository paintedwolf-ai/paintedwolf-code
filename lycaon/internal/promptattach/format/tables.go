package format

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
