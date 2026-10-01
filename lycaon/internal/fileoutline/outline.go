package fileoutline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/filekind"
	"github.com/lycaon/lycaon/internal/logoutline"
	"github.com/lycaon/lycaon/internal/repomap"
	"github.com/lycaon/lycaon/internal/syntaxhealth"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tsparse"
)

// Symbol is one structural entry with a 1-based line number.
type Symbol struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Line int    `json:"line"`
}

// Result is one file's structural outline and parse health.
type Result struct {
	Path         string               `json:"path"`
	TotalLines   int                  `json:"total_lines"`
	SizeBytes    int64                `json:"size_bytes"`
	Source       string               `json:"source"` // tree_sitter | tree_sitter+regex | markdown | regex | log | diff
	Language     string               `json:"language,omitempty"`
	Symbols      []Symbol             `json:"symbols"`
	LogDigest    *logoutline.Digest   `json:"log_digest,omitempty"`
	Parses       *bool                `json:"parses,omitempty"`
	Errors       []SyntaxDiagnostic   `json:"errors,omitempty"`
	ParseFailure *tsparse.Failure     `json:"parse_failure,omitempty"`
	Diagnostics  *repomap.Diagnostics `json:"diagnostics,omitempty"`
	// Definitions retain source spans for internal consumers.
	Definitions []repomap.DefinitionSpan `json:"-"`
}

// embeddedScriptLanguages require a regex pass for inline definitions.
var embeddedScriptLanguages = map[string]bool{
	"html":   true,
	"vue":    true,
	"svelte": true,
	"astro":  true,
}

var markupStructureKinds = map[string]bool{
	"section": true,
	"tag":     true,
}

var regexOutlinePatterns = []struct {
	kind string
	re   *regexp.Regexp
}{
	{"class", regexp.MustCompile(`(?m)^(?:export\s+)?(?:abstract\s+)?class\s+([A-Za-z_]\w*)`)},
	{"function", regexp.MustCompile(`(?m)^(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_]\w*)`)},
	{"method", regexp.MustCompile(`(?m)^\s+(?:async\s+)?def\s+([A-Za-z_]\w*)`)},
	{"function", regexp.MustCompile(`(?m)^def\s+([A-Za-z_]\w*)`)},
	{"class", regexp.MustCompile(`(?m)^class\s+([A-Za-z_]\w*)`)},
	{"type", regexp.MustCompile(`(?m)^type\s+([A-Za-z_]\w*)`)},
	{"struct", regexp.MustCompile(`(?m)^type\s+([A-Za-z_]\w*)\s+struct`)},
	{"function", regexp.MustCompile(`(?m)^func\s+(?:\([^)]+\)\s+)?([A-Za-z_]\w*)`)},
	{"interface", regexp.MustCompile(`(?m)^(?:export\s+)?interface\s+([A-Za-z_]\w*)`)},
	{"const", regexp.MustCompile(`(?m)^(?:export\s+)?const\s+([A-Za-z_]\w*)`)},
}

// Build returns a structural outline for one project file.
func Build(ctx context.Context, projectDir, relPath string) (Result, error) {
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	rel := filepath.ToSlash(strings.TrimSpace(relPath))
	if rel == "" {
		return Result{}, fmt.Errorf("fileoutline: path required")
	}
	absRoot, err := filepath.Abs(strings.TrimSpace(projectDir))
	if err != nil {
		return Result{}, err
	}
	fullPath := filepath.Join(absRoot, filepath.FromSlash(rel))
	info, err := os.Stat(fullPath)
	if err != nil {
		return Result{}, err
	}
	if info.IsDir() {
		return Result{}, fmt.Errorf("fileoutline: path is a directory: %s", rel)
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return Result{}, err
	}
	return BuildContent(ctx, rel, data, info.Size())
}

// BuildContent outlines already-read file bytes.
func BuildContent(ctx context.Context, relPath string, data []byte, sizeBytes int64) (Result, error) {
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	rel := filepath.ToSlash(strings.TrimSpace(relPath))
	if rel == "" {
		return Result{}, fmt.Errorf("fileoutline: path required")
	}
	if sizeBytes <= 0 {
		sizeBytes = int64(len(data))
	}
	res := AnalyzeText(ctx, rel, data)
	res.Path = rel
	res.SizeBytes = sizeBytes
	return res, nil
}

// AnalyzeText returns cached structural analysis for source bytes.
func AnalyzeText(ctx context.Context, filenameHint string, src []byte) Result {
	return processAnalysisCache.analyze(ctx, filenameHint, src, func() Result {
		return analyzeText(ctx, filenameHint, src)
	})
}

func analyzeText(ctx context.Context, filenameHint string, src []byte) Result {
	text := string(src)
	res := Result{
		TotalLines: toolkit.CountLines(text),
		SizeBytes:  int64(len(src)),
		Symbols:    []Symbol{},
	}
	base := filepath.Base(strings.TrimSpace(filenameHint))
	if ext := strings.ToLower(filepath.Ext(base)); ext == ".diff" || ext == ".patch" {
		outlineDiff(&res, text)
		return res
	}
	detectRes := filekind.Detect(ctx, filekind.DetectReq{
		Filename:   base,
		HeadSample: src,
		Mode:       filekind.DepthDeep,
	})
	if detectRes.Grammar != nil && detectRes.Grammar.Name == "diff" {
		outlineDiff(&res, text)
		return res
	}
	if detectRes.Log != logoutline.FormatNone {
		if digest, err := logoutline.BuildDigest(detectRes.Log, src); err == nil {
			res.Source = "log"
			res.LogDigest = digest
			return res
		}
	}
	hint := base
	if hint == "" {
		hint = strings.TrimSpace(filenameHint)
	}
	tags := repomap.TagsFromBytes(ctx, hint, src)
	// Direct document analysis exceeds the repository-map size cap.
	if tags.Skip.Oversized != 0 && detectRes.Grammar != nil {
		var err error
		tags.Definitions, tags.Language, _, err = repomap.DefinitionSpans(ctx, detectRes.Grammar.Name, hint, src)
		if errors.Is(err, repomap.ErrDefinitionIncomplete) {
			tags.Skip.ParseIncomplete++
		} else if err != nil {
			tags.Skip.ParseFailed++
		}
		errors.As(err, &tags.Failure)
	}
	fillFromTags(ctx, &res, text, src, tags)
	if isConfigLanguage(res.Language, base) {
		enrichConfigOutline(&res, text)
	}
	return res
}

// fillFromTags projects parse health and symbols.
func fillFromTags(ctx context.Context, res *Result, text string, src []byte, outcome repomap.FileTagOutcome) {
	res.Language = outcome.Language
	res.ParseFailure = outcome.Failure
	res.Definitions = append([]repomap.DefinitionSpan(nil), outcome.Definitions...)
	if outcome.Language == "markdown" {
		res.Source = "markdown"
		for _, definition := range outcome.Definitions {
			res.Symbols = append(res.Symbols, Symbol{Kind: definition.Kind, Name: definition.Name, Line: definition.StartRow + 1})
		}
		res.Diagnostics = outlineDiagnostics(res.Source, outcome.Skip)
		return
	}
	// Empty language means parse health is unknown.
	if outcome.Language != "" && outcome.Failure == nil {
		report := syntaxhealth.Analyze(ctx, outcome.Language, "", src, tsparse.Analysis)
		if report.Failure != nil {
			res.ParseFailure = report.Failure
		}
		if report.Status == syntaxhealth.StatusClean || report.Status == syntaxhealth.StatusBroken {
			ok := report.Status == syntaxhealth.StatusClean
			res.Parses = &ok
			res.Errors = outlineSyntaxDiagnostics(report.Diagnostics)
		}
	}
	if len(outcome.Tags) > 0 {
		res.Source = "tree_sitter"
		for _, tag := range outcome.Tags {
			res.Symbols = append(res.Symbols, Symbol{
				Kind: tag.Kind,
				Name: tag.Name,
				Line: tag.Line,
			})
		}
		if embeddedScriptLanguages[outcome.Language] && markupOnly(res.Symbols) {
			res.Symbols = mergeSymbols(res.Symbols, regexSymbols(text))
			res.Source = "tree_sitter+regex"
		}
		return
	}
	res.Source = "regex"
	res.Symbols = regexSymbols(text)
	res.Diagnostics = outlineDiagnostics(res.Source, outcome.Skip)
}

// markupOnly reports whether every symbol is document structure.
func markupOnly(symbols []Symbol) bool {
	for _, s := range symbols {
		if !markupStructureKinds[s.Kind] {
			return false
		}
	}
	return true
}

// mergeSymbols keeps primary symbols on line collisions and sorts the result.
func mergeSymbols(primary, extra []Symbol) []Symbol {
	taken := make(map[int]struct{}, len(primary))
	for _, s := range primary {
		taken[s.Line] = struct{}{}
	}
	out := append([]Symbol(nil), primary...)
	for _, s := range extra {
		if _, ok := taken[s.Line]; ok {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func regexSymbols(text string) []Symbol {
	lines := strings.Split(text, "\n")
	seen := map[string]struct{}{}
	var out []Symbol
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
			continue
		}
		for _, pat := range regexOutlinePatterns {
			m := pat.re.FindStringSubmatch(line)
			if len(m) < 2 {
				continue
			}
			name := strings.TrimSpace(m[1])
			if name == "" {
				continue
			}
			key := pat.kind + ":" + name + ":" + fmt.Sprint(i+1)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, Symbol{Kind: pat.kind, Name: name, Line: i + 1})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Name < out[j].Name
	})
	return out
}
