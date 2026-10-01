package guidance_test

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
)

var (
	pathLineCitationRE = regexp.MustCompile(`[^\s\])>'"]+:\d+`)
	backtickPathRE     = regexp.MustCompile("`([^`]+)`")
	markdownLinkRE     = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	urlCitationRE      = regexp.MustCompile(`https?://[^\s\])>'"]+`)
	fencedCodeBlockRE  = regexp.MustCompile("(?s)```.*?```")
)

var knownCitationExts = map[string]struct{}{
	".go": {}, ".py": {}, ".ts": {}, ".tsx": {}, ".js": {}, ".jsx": {},
	".md": {}, ".yaml": {}, ".yml": {}, ".json": {}, ".sql": {},
	".rs": {}, ".java": {}, ".kt": {}, ".rb": {}, ".php": {},
	".c": {}, ".h": {}, ".cpp": {}, ".hpp": {}, ".cs": {},
	".sh": {}, ".bash": {}, ".zsh": {},
}

const (
	bareLeadTrimSet  = "\"'`*([{<«"
	bareTrailTrimSet = "\"'`*.,;:!?)]}>»…"
)

type pathLineCitation struct {
	Raw    string
	Path   string
	Line   int
	Offset int
}

type citations struct {
	PathLines []pathLineCitation
	Paths     []string
	URLs      []string
}

func extractCitations(prose string) citations {
	prose = strings.TrimSpace(prose)
	if prose == "" {
		return citations{}
	}

	var out citations
	seenPathLine := map[string]struct{}{}
	for _, loc := range pathLineCitationRE.FindAllStringIndex(prose, -1) {
		raw := prose[loc[0]:loc[1]]
		pathPart, line, ok := evidence.SplitPathLineToken(raw)
		if !ok {
			continue
		}
		key := raw
		if _, dup := seenPathLine[key]; dup {
			continue
		}
		seenPathLine[key] = struct{}{}
		out.PathLines = append(out.PathLines, pathLineCitation{
			Raw:    raw,
			Path:   filepath.ToSlash(strings.TrimSpace(pathPart)),
			Line:   line,
			Offset: loc[0],
		})
	}

	seenPath := map[string]struct{}{}
	addPath := func(token string) {
		token = filepath.ToSlash(strings.TrimSpace(token))
		if token == "" {
			return
		}
		if _, dup := seenPath[token]; dup {
			return
		}
		seenPath[token] = struct{}{}
		out.Paths = append(out.Paths, token)
	}

	for _, loc := range backtickPathRE.FindAllStringSubmatchIndex(prose, -1) {
		if len(loc) < 4 {
			continue
		}
		inner := prose[loc[2]:loc[3]]
		if !isDelimitedPathCitation(inner) {
			continue
		}
		addPath(inner)
	}

	for _, m := range markdownLinkRE.FindAllStringSubmatch(prose, -1) {
		if len(m) < 2 || !isDelimitedPathCitation(m[1]) {
			continue
		}
		addPath(m[1])
	}

	for _, field := range strings.Fields(fencedCodeBlockRE.ReplaceAllString(prose, " ")) {
		token := trimBareCitationToken(field)
		if !isBarePathCitation(token) {
			continue
		}
		addPath(token)
	}

	seenURL := map[string]struct{}{}
	for _, loc := range urlCitationRE.FindAllStringIndex(prose, -1) {
		raw := prose[loc[0]:loc[1]]
		if _, dup := seenURL[raw]; dup {
			continue
		}
		seenURL[raw] = struct{}{}
		out.URLs = append(out.URLs, raw)
	}

	return out
}

func isDelimitedPathCitation(inner string) bool {
	inner = strings.TrimSpace(inner)
	if inner == "" {
		return false
	}
	if strings.ContainsAny(inner, " \t\n\r\f\v") {
		return false
	}
	if strings.Contains(inner, "://") {
		return false
	}
	if strings.Contains(inner, "/") {
		return true
	}
	ext := strings.ToLower(filepath.Ext(inner))
	if ext == "" {
		return false
	}
	_, ok := knownCitationExts[ext]
	return ok
}

func isBarePathCitation(token string) bool {
	if token == "" || strings.Contains(token, "://") {
		return false
	}
	if !strings.Contains(token, "/") {
		return false
	}
	if strings.HasPrefix(token, "-") {
		return false
	}
	if strings.ContainsAny(token, "`$()<>|;&*?:=,'\"\\{}[]") {
		return false
	}
	ext := strings.ToLower(filepath.Ext(token))
	if ext == "" {
		return false
	}
	_, ok := knownCitationExts[ext]
	return ok
}

func trimBareCitationToken(token string) string {
	for {
		trimmed := strings.TrimLeft(strings.TrimRight(token, bareTrailTrimSet), bareLeadTrimSet)
		if trimmed == token {
			return trimmed
		}
		token = trimmed
	}
}

func TestExtractCitations_pathLineAndBacktick(t *testing.T) {
	prose := "Failure at `internal/auth/handler.go` line internal/auth/handler.go:42 — see https://example.com/docs"
	cites := extractCitations(prose)

	if len(cites.PathLines) != 1 || cites.PathLines[0].Path != "internal/auth/handler.go" || cites.PathLines[0].Line != 42 {
		t.Fatalf("path:line = %+v", cites.PathLines)
	}
	if len(cites.Paths) != 1 || cites.Paths[0] != "internal/auth/handler.go" {
		t.Fatalf("paths = %v", cites.Paths)
	}
	if len(cites.URLs) != 1 || cites.URLs[0] != "https://example.com/docs" {
		t.Fatalf("urls = %v", cites.URLs)
	}
}

func TestExtractCitations_bareRepoPath(t *testing.T) {
	prose := "Entry point is lycaon/cmd/lycaon/main.go, wired by lycaon/internal/app/build.go."
	cites := extractCitations(prose)
	want := []string{"lycaon/cmd/lycaon/main.go", "lycaon/internal/app/build.go"}
	if len(cites.Paths) != len(want) {
		t.Fatalf("paths = %v want %v", cites.Paths, want)
	}
	for i, p := range want {
		if cites.Paths[i] != p {
			t.Fatalf("paths[%d] = %q want %q", i, cites.Paths[i], p)
		}
	}
}

func TestExtractCitations_bareTokenPunctuationStripped(t *testing.T) {
	prose := "Schemas live in (lycaon/config/packs/painted-wolf/platform/tools/schemas/read.yaml); see **docs/agent-contract.md** and \"docs/openapi/paths/scan.yaml\"."
	cites := extractCitations(prose)
	want := []string{
		"lycaon/config/packs/painted-wolf/platform/tools/schemas/read.yaml",
		"docs/agent-contract.md",
		"docs/openapi/paths/scan.yaml",
	}
	if len(cites.Paths) != len(want) {
		t.Fatalf("paths = %v want %v", cites.Paths, want)
	}
	for i, p := range want {
		if cites.Paths[i] != p {
			t.Fatalf("paths[%d] = %q want %q", i, cites.Paths[i], p)
		}
	}
}

func TestExtractCitations_bareTokenRejectsNarrativeSlashes(t *testing.T) {
	prose := "Use read/grep on the dispatch/review split under internal/session with docs/openapi/** at TLS1.2/1.3 via --config=path/x.yaml and promote_overlay/reject_overlay"
	cites := extractCitations(prose)
	if len(cites.Paths) != 0 {
		t.Fatalf("expected no bare path citations, got %v", cites.Paths)
	}
}

func TestExtractCitations_bareBasenameStaysNarrative(t *testing.T) {
	cites := extractCitations("Edit main.go and check Taskfile.yml first")
	if len(cites.Paths) != 0 {
		t.Fatalf("expected no path citations, got %v", cites.Paths)
	}
}

func TestExtractCitations_markdownLinkTarget(t *testing.T) {
	prose := "See [the handler](internal/auth/handler.go) and [docs](https://example.com/docs)"
	cites := extractCitations(prose)
	if len(cites.Paths) != 1 || cites.Paths[0] != "internal/auth/handler.go" {
		t.Fatalf("paths = %v", cites.Paths)
	}
	if len(cites.URLs) != 1 {
		t.Fatalf("urls = %v", cites.URLs)
	}
}

func TestExtractCitations_backtickURLNotAPath(t *testing.T) {
	cites := extractCitations("Fetched `https://example.com/docs/page.md` for context")
	if len(cites.Paths) != 0 {
		t.Fatalf("expected no path citations for backticked URL, got %v", cites.Paths)
	}
}

func TestExtractCitations_dedupAcrossDelimitedAndBare(t *testing.T) {
	cites := extractCitations("`pkg/main.go` is the entry; pkg/main.go defines wiring")
	if len(cites.Paths) != 1 || cites.Paths[0] != "pkg/main.go" {
		t.Fatalf("paths = %v", cites.Paths)
	}
}

func TestExtractCitations_ignoresBareProse(t *testing.T) {
	cites := extractCitations("see internal/foo for details")
	if len(cites.PathLines)+len(cites.Paths)+len(cites.URLs) != 0 {
		t.Fatalf("expected no citations, got %+v", cites)
	}
}

func TestExtractCitations_knownExtensionWithoutSlash(t *testing.T) {
	cites := extractCitations("Edit `main.go` only")
	if len(cites.Paths) != 1 || cites.Paths[0] != "main.go" {
		t.Fatalf("paths = %v", cites.Paths)
	}
}

func TestExtractCitations_ignoresCommandBackticks(t *testing.T) {
	prose := "Run it with `.venv/bin/python game.py` or `bash run.sh`; saw `cbreak/nocbreak ERR`."
	cites := extractCitations(prose)
	if len(cites.Paths) != 0 {
		t.Fatalf("expected no path citations, got %v", cites.Paths)
	}
}

func TestExtractCitations_ignoresFencedCodeBlock(t *testing.T) {
	prose := "To play:\n```bash\n./run.sh\n.venv/bin/python game.py\nsrc/util/helpers.py\n```\nDone."
	cites := extractCitations(prose)
	if len(cites.Paths) != 0 {
		t.Fatalf("fenced block content leaked as path citations: %v", cites.Paths)
	}
	for _, p := range cites.Paths {
		if strings.ContainsAny(p, " \t\n") {
			t.Fatalf("multi-token span leaked as path citation: %q", p)
		}
	}
}
