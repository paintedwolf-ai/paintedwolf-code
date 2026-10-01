package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/repomap"
)

// unit is one harvested definition: where it is and what the engine could read.
type unit struct {
	Repo   string `json:"repo"`
	File   string `json:"file"`
	Line   int    `json:"line"`
	Symbol string `json:"symbol"`
	Kind   string `json:"kind"`
	Lang   string `json:"lang"`
	Doc    string `json:"doc,omitempty"`
	Code   string `json:"code"`
}

// pair is one evaluation example: a task written for a unit.
type pair struct {
	Repo   string `json:"repo"`
	File   string `json:"file"`
	Line   int    `json:"line"`
	Symbol string `json:"symbol"`
	// Task is a natural-language request; Query is what a person would type
	// into project search for the same unit.
	Task   string `json:"task"`
	Query  string `json:"query,omitempty"`
	Lang   string `json:"lang,omitempty"`
	Source string `json:"source,omitempty"`
}

const (
	unitCodeRunes    = 1200
	unitDocLines     = 6
	unitsPerFileMax  = 60
	harvestMaxFiles  = 3000
	harvestFileBytes = 256 * 1024
)

func runHarvest(ctx context.Context, args []string) error {
	fs := newFlags("harvest")
	repo := fs.String("repo", "", "repository root to harvest")
	name := fs.String("name", "", "repository name recorded on every unit")
	out := fs.String("out", "", "units JSONL to write")
	include := fs.String("include", "", "comma-separated repo-relative directories; empty harvests the whole root")
	maxFiles := fs.Int("max-files", harvestMaxFiles, "stop after this many source files")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *repo == "" || *name == "" || *out == "" {
		return errors.New("harvest needs --repo, --name, and --out")
	}
	var subpaths []string
	for _, dir := range strings.Split(*include, ",") {
		if dir = strings.TrimSpace(dir); dir != "" {
			subpaths = append(subpaths, dir)
		}
	}
	units, stats, err := harvestUnits(ctx, *repo, *name, subpaths, *maxFiles)
	if err != nil {
		return err
	}
	if err := writeJSONL(*out, units); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "harvested %d units from %d files (%s)\n", len(units), stats.FilesScanned, *name)
	return nil
}

func harvestUnits(ctx context.Context, root, name string, subpaths []string, maxFiles int) ([]unit, repomap.SourceWalkStats, error) {
	var units []unit
	stats, err := repomap.WalkSourceFiles(ctx, repomap.SourceWalkOptions{
		Root: root, Subpaths: subpaths, Recursive: true, MaxFiles: maxFiles, MaxFileBytes: harvestFileBytes,
	}, func(rel, abs string) error {
		units = append(units, fileUnits(ctx, name, rel, abs)...)
		return ctx.Err()
	})
	if err != nil {
		return nil, stats, fmt.Errorf("walk %s: %w", root, err)
	}
	return units, stats, nil
}

// fileUnits returns a file's rankable definitions. A file that cannot be read
// or parsed contributes none rather than ending the harvest.
func fileUnits(ctx context.Context, repo, rel, abs string) []unit {
	src, err := os.ReadFile(abs) //nolint:gosec // G304 — the walk yields paths under the chosen root
	if err != nil {
		return nil
	}
	spans, lang, supported, err := repomap.DefinitionSpans(ctx, "", rel, src)
	if err != nil || !supported || !codeLanguage(lang) {
		return nil
	}
	lines := strings.Split(string(src), "\n")
	var units []unit
	for i, span := range spans {
		if i >= unitsPerFileMax {
			break
		}
		if strings.TrimSpace(span.Name) == "" {
			continue
		}
		units = append(units, unit{
			Repo: repo, File: rel, Line: span.StartRow + 1, Symbol: span.Name, Kind: span.Kind, Lang: lang,
			Doc:  leadingComment(lang, lines, span.StartRow),
			Code: boundRunes(string(src[span.StartByte:min(span.EndByte, len(src))]), unitCodeRunes),
		})
	}
	return units
}

// configLanguages have definitions but no prose worth ranking against.
var configLanguages = map[string]bool{
	"toml": true, "yaml": true, "json": true, "markdown": true, "html": true, "css": true, "xml": true, "ini": true,
}

func codeLanguage(lang string) bool {
	return !configLanguages[strings.ToLower(lang)]
}

// commentMarkers are the line-comment prefixes of a language family. Rust
// attributes and C preprocessor lines start with # but are not comments.
func commentMarkers(lang string) []string {
	switch strings.ToLower(lang) {
	case "python", "ruby", "shell", "bash", "perl", "elixir", "r":
		return []string{"#"}
	case "lua", "haskell", "sql":
		return []string{"--"}
	default:
		return []string{"///", "//!", "//", "/**", "/*", "*/", "*"}
	}
}

// leadingComment returns the comment lines directly above a definition.
func leadingComment(lang string, lines []string, row int) string {
	markers := commentMarkers(lang)
	var out []string
	for i := row - 1; i >= 0 && len(out) < unitDocLines; i-- {
		text := strings.TrimSpace(lines[i])
		if text == "" {
			break
		}
		marker := ""
		for _, m := range markers {
			if strings.HasPrefix(text, m) {
				marker = m
				break
			}
		}
		if marker == "" {
			// Decorators and attributes sit between the comment and the definition.
			if strings.HasPrefix(text, "#[") || strings.HasPrefix(text, "@") {
				continue
			}
			break
		}
		out = append(out, strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, marker), "*/")))
	}
	slices.Reverse(out)
	return strings.TrimSpace(strings.Join(out, " "))
}

func boundRunes(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit])
}

// corpus is one repository's harvested units, indexed by file.
type corpus struct {
	name   string
	root   string
	byFile map[string][]unit
	files  []string
}

func loadCorpus(unitsPath, name, root string) (*corpus, error) {
	var units []unit
	if err := readJSONL(unitsPath, &units); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	c := &corpus{name: name, root: abs, byFile: map[string][]unit{}}
	for _, u := range units {
		if u.Repo != name {
			continue
		}
		if _, seen := c.byFile[u.File]; !seen {
			c.files = append(c.files, u.File)
		}
		c.byFile[u.File] = append(c.byFile[u.File], u)
	}
	sort.Strings(c.files)
	if len(c.files) == 0 {
		return nil, fmt.Errorf("no units for repository %q in %s", name, unitsPath)
	}
	return c, nil
}

// filesUnder lists the corpus files inside dir in path order, as a gather
// would, keeping the target inside the limit. Path order matters: a site's
// stable sort keeps input order on ties, so the target must not be placed
// where a tie would favour it.
func (c *corpus) filesUnder(dir, target string, limit int) []string {
	prefix := strings.TrimSuffix(path.Clean(dir), "/") + "/"
	if dir == "." || dir == "" {
		prefix = ""
	}
	var out []string
	for _, f := range c.files {
		if strings.HasPrefix(f, prefix) {
			out = append(out, f)
		}
	}
	if limit > 0 && len(out) > limit {
		kept := out[:limit]
		if !slices.Contains(kept, target) {
			kept = append(kept[:limit-1], target)
			sort.Strings(kept)
		}
		out = kept
	}
	return out
}

func (c *corpus) read(rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(c.root, filepath.FromSlash(rel)))
}

func writeJSONL[T any](name string, rows []T) (err error) {
	if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		return err
	}
	f, err := os.Create(name) //nolint:gosec // G304 — output path chosen on the command line
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			return err
		}
	}
	return w.Flush()
}

func readJSONL[T any](name string, into *[]T) error {
	f, err := os.Open(name) //nolint:gosec // G304 — input path chosen on the command line
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var row T
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		*into = append(*into, row)
	}
	return sc.Err()
}
