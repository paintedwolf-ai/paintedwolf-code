// Package repomap builds compact structural and symbol views of a directory tree.
package repomap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/filekind"
	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

const (
	defaultMaxBytes         = 200000
	DefaultWalkMaxFileBytes = 256 * 1024
	DefaultWalkMaxFiles     = 4000

	// parseThreshold switches from symbols to a directory map.
	parseThreshold = 400

	emptyRepoMapHintCode = "REPO_MAP_EMPTY"
	noFilesHintCode      = "REPO_MAP_NO_FILES"
)

// Tag identifies a definition; Kind omits the query capture's "definition." prefix.
type Tag struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Language string `json:"lang"`
	// Signature is the definition's source line and Doc the comment above it,
	// both bounded. They feed task ordering and never the serialized view.
	Signature string `json:"-"`
	Doc       string `json:"-"`
}

// SkipStats counts why candidate files did not contribute tags during a parse.
type SkipStats struct {
	NoGrammar    int `json:"no_grammar,omitempty"`
	NoTagger     int `json:"no_tagger,omitempty"`
	NoTags       int `json:"no_tags,omitempty"`
	Oversized    int `json:"oversized,omitempty"`
	Unreadable   int `json:"unreadable,omitempty"`
	LangFiltered int `json:"lang_filtered,omitempty"`
	ParseFailed  int `json:"parse_failed,omitempty"`
	// ParseIncomplete counts files omitted because parsing did not finish.
	ParseIncomplete int `json:"parse_incomplete,omitempty"`
}

// Diagnostics explains empty or partial repo_map results.
type Diagnostics struct {
	SkipReasons SkipStats `json:"skip_reasons,omitempty"`
	Hint        string    `json:"hint,omitempty"`
	HintCode    string    `json:"hint_code,omitempty"`
}

// Node is one row in a structural directory map.
// ZoomIn marks children collapsed behind a scoped scan.
type Node struct {
	Path     string   `json:"path"`               // repo-relative, slash-separated
	Type     string   `json:"type"`               // "dir" | "file"
	Files    int      `json:"files,omitempty"`    // dir: regular files in subtree
	Bytes    int64    `json:"bytes,omitempty"`    // subtree bytes or file size
	Langs    []string `json:"langs,omitempty"`    // languages present, by extension
	Children []*Node  `json:"children,omitempty"` // expanded child dirs/files
	ZoomIn   bool     `json:"zoom_in,omitempty"`  // subtree summarized — scope path here to drill in
}

// Snapshot is a symbol or directory-map view.
type Snapshot struct {
	ParseFailures          []tsparse.FileFailure `json:"parse_failures,omitempty"`
	ParseFailuresTruncated bool                  `json:"parse_failures_truncated,omitempty"`
	Root                   string                `json:"root"`
	Path                   string                `json:"path"` // scoped subpath; "." = whole root
	View                   string                `json:"view"` // "tags" | "map"
	Languages              []string              `json:"languages"`
	FilesScanned           int                   `json:"files_scanned"`          // files visited by the walk; shallow views count immediate entries
	SourceFiles            int                   `json:"source_files"`           // regular files in scope
	FilesParsed            int                   `json:"files_parsed,omitempty"` // files actually read + parsed (tags view)
	MaxBytes               int                   `json:"max_bytes"`

	// View == "tags".
	Tags       []Tag `json:"tags,omitempty"`
	TotalDefs  int   `json:"total_defs,omitempty"`
	Offset     int   `json:"offset,omitempty"`
	NextOffset *int  `json:"next_offset,omitempty"`
	Truncated  bool  `json:"truncated,omitempty"`

	// View == "map".
	Tree *Node `json:"tree,omitempty"`

	Diagnostics      *Diagnostics `json:"diagnostics,omitempty"`
	OrientationBrief string       `json:"orientation_brief,omitempty"`
}

// Options control a single Build call.
type Options struct {
	Root      string   // absolute project root; required
	Subpath   string   // optional subpath under Root; empty = whole root
	Depth     int      // max map expansion depth from the scoped path; 0 = auto (budget-bounded)
	Languages []string // language-name filter (lowercased); nil = all
	MaxBytes  int      // soft cap on serialized size in bytes; 0 = default
	Offset    int      // tags view: skip this many definitions before packing (pagination)
	// ParseThreshold overrides the source-file count above which a scope returns a
	// structural map instead of parsed symbols; 0 = default (parseThreshold).
	ParseThreshold int
	// ShallowRootMap returns immediate children without parsing.
	ShallowRootMap bool
	// PathIncluded reports whether relSlash (repo-relative, slash-separated) may be scanned.
	// When nil, all paths under Root except engine/VCS metadata skips are included.
	PathIncluded func(relSlash string, isDir bool) bool
	// PruneNestedVCS prunes subdirectories that contain a .git entry.
	PruneNestedVCS bool
	// OnNestedRepoPruned is called once per pruned nested VCS root during the walk.
	OnNestedRepoPruned func(abs string)
	// Task orders each packed tags page by relevance: lexical over file and
	// symbol, with Rerank's engine blended in. Paging itself stays
	// alphabetical, so a page's membership never depends on the task.
	Task   string
	Rerank decide.Reranker
}

// InventoryEntry is filesystem metadata from an immutable tree generation.
type InventoryEntry struct {
	Path  string
	Name  string
	Size  int64
	IsDir bool
}

// candidate is a regular file that has not been read or parsed.
type candidate struct {
	rel  string // repo-relative, slash-separated
	lang string
	size int64
}

// scannedFile is one parsed source file and the definitions it contributed.
type scannedFile struct {
	rel  string
	lang string
	tags []Tag
}

// taggerSlot serializes one grammar tagger.
type taggerSlot struct {
	err error
	tg  *gotreesitter.Tagger
	mu  sync.Mutex
}

type taggerCache struct {
	mu sync.Mutex
	by map[string]*taggerSlot
}

type tagParseResult struct {
	tags     []gotreesitter.Tag
	noTagger bool
	failure  *tsparse.Failure
}

func (c *taggerCache) tag(ctx context.Context, entry grammars.LangEntry, src []byte) (result tagParseResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			cause := fmt.Errorf("source analysis panic: %v", recovered)
			result = tagParseResult{failure: &tsparse.Failure{Reason: "panic", Language: entry.Name, SourceBytes: len(src), Cause: cause, Detail: cause.Error()}}
		}
	}()
	if entry.Name == "markdown" {
		return markdownTags(src)
	}
	slot := c.slot(entry)
	if slot != nil && slot.err != nil {
		return tagParseResult{failure: &tsparse.Failure{Reason: "tagger_configuration", Language: entry.Name, SourceBytes: len(src), Cause: slot.err, Detail: slot.err.Error()}}
	}
	if slot == nil || slot.tg == nil {
		return tagParseResult{noTagger: true}
	}
	slot.mu.Lock()
	defer slot.mu.Unlock()
	raw, err := tagWithRecover(slot.tg, func() (*gotreesitter.Tree, error) {
		return tsparse.Parse(ctx, entry.Language(), src, tsparse.Analysis)
	})
	if err != nil {
		var failure *tsparse.Failure
		errors.As(err, &failure)
		if failure == nil {
			failure = &tsparse.Failure{Reason: "parser_error", Cause: err, Detail: err.Error()}
		}
		failure.Language, failure.SourceBytes = entry.Name, len(src)
		if failure.Cause != nil {
			failure.Detail = failure.Cause.Error()
		}
		return tagParseResult{failure: failure}
	}
	return tagParseResult{tags: raw}
}

func (c *taggerCache) slot(entry grammars.LangEntry) *taggerSlot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.by == nil {
		c.by = make(map[string]*taggerSlot)
	}
	if s, ok := c.by[entry.Name]; ok {
		return s
	}
	q := grammars.ResolveTagsQuery(entry)
	if strings.TrimSpace(q) == "" {
		c.by[entry.Name] = &taggerSlot{}
		return c.by[entry.Name]
	}
	t, err := gotreesitter.NewTagger(entry.Language(), q)
	if err != nil {
		c.by[entry.Name] = &taggerSlot{err: err}
		return c.by[entry.Name]
	}
	c.by[entry.Name] = &taggerSlot{tg: t}
	return c.by[entry.Name]
}

// Build returns the most detailed view that fits the scope.
func Build(ctx context.Context, opts Options) (*Snapshot, error) {
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		return nil, errors.New("repomap: root required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("repomap: abs root: %w", err)
	}
	scopeRel := "."
	walkRoot := absRoot
	if sub := strings.TrimSpace(opts.Subpath); sub != "" {
		scopeRel = filepath.ToSlash(sub)
		walkRoot = filepath.Join(absRoot, sub)
	}
	st, err := os.Stat(walkRoot)
	if err != nil {
		return nil, fmt.Errorf("repomap: stat root: %w", err)
	}

	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	langFilter := map[string]bool{}
	for _, n := range opts.Languages {
		n = strings.TrimSpace(strings.ToLower(n))
		if n != "" {
			langFilter[n] = true
		}
	}

	snap := &Snapshot{Root: absRoot, Path: scopeRel, MaxBytes: maxBytes, Languages: []string{}}

	if opts.ShallowRootMap && st.IsDir() {
		return buildShallowRootMap(ctx, snap, walkRoot, scopeRel, opts)
	}

	// A file-scoped path is already a leaf — read and parse just that file.
	if !st.IsDir() {
		files, skip, failures, err := parseCandidates(ctx, absRoot, []candidate{{rel: scopeRel, size: st.Size()}}, langFilter)
		if err != nil {
			return nil, err
		}
		snap.ParseFailures = failures
		snap.ParseFailuresTruncated = skip.ParseIncomplete+skip.ParseFailed > len(failures)
		snap.FilesScanned, snap.SourceFiles, snap.FilesParsed = 1, 1, len(files)
		return tagsSnapshot(ctx, snap, files, opts, maxBytes, skip), nil
	}

	state := &buildWalkState{
		ctx: ctx, opts: opts, absRoot: absRoot, walkRoot: walkRoot, langFilter: langFilter,
	}
	if err := state.structuralScan(); err != nil {
		return nil, err
	}
	return finishBuild(ctx, snap, opts, state.candidates, state.scanned, langFilter)
}

// BuildFromInventory builds an adaptive repo map from catalog metadata.
func BuildFromInventory(ctx context.Context, opts Options, entries []InventoryEntry) (*Snapshot, error) {
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		return nil, errors.New("repomap: root required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("repomap: abs root: %w", err)
	}
	scopeRel := "."
	if sub := strings.TrimSpace(opts.Subpath); sub != "" {
		scopeRel = filepath.ToSlash(sub)
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	langFilter := map[string]bool{}
	for _, name := range opts.Languages {
		name = strings.TrimSpace(strings.ToLower(name))
		if name != "" {
			langFilter[name] = true
		}
	}
	snap := &Snapshot{Root: absRoot, Path: scopeRel, MaxBytes: maxBytes, Languages: []string{}}
	if opts.ShallowRootMap {
		return buildShallowInventoryMap(ctx, snap, scopeRel, opts, entries)
	}
	candidates := make([]candidate, 0, min(len(entries), DefaultWalkMaxFiles))
	scanned := 0
	for _, item := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(filepath.Clean(item.Path), "./"))
		if rel == "" || rel == "." || !inventoryEntryUnderScope(rel, scopeRel) {
			continue
		}
		if opts.PathIncluded != nil && !opts.PathIncluded(rel, item.IsDir) {
			continue
		}
		if item.IsDir {
			continue
		}
		scanned++
		name := item.Name
		if name == "" {
			name = filepath.Base(rel)
		}
		entry := filekind.Detect(ctx, filekind.DetectReq{
			Filename: name,
			Mode:     filekind.DepthShallow,
		}).Grammar
		if entry == nil {
			if len(langFilter) == 0 {
				candidates = append(candidates, candidate{rel: rel, size: item.Size})
			}
			continue
		}
		if len(langFilter) > 0 && !langFilter[strings.ToLower(entry.Name)] {
			continue
		}
		candidates = append(candidates, candidate{rel: rel, lang: entry.Name, size: item.Size})
	}
	return finishBuild(ctx, snap, opts, candidates, scanned, langFilter)
}

func finishBuild(
	ctx context.Context,
	snap *Snapshot,
	opts Options,
	candidates []candidate,
	scanned int,
	langFilter map[string]bool,
) (*Snapshot, error) {
	snap.FilesScanned = scanned
	snap.SourceFiles = len(candidates)
	snap.Languages = candidateLangs(candidates)

	if len(candidates) == 0 {
		snap.View = "tags"
		snap.Tags = []Tag{}
		snap.Diagnostics = emptyDiagnostics(ctx, snap)
		return snap, nil
	}

	threshold := parseThreshold
	if opts.ParseThreshold > 0 {
		threshold = opts.ParseThreshold
	}
	if scanned <= threshold && len(candidates) <= threshold {
		files, skip, failures, err := parseCandidates(ctx, snap.Root, candidates, langFilter)
		if err != nil {
			return nil, err
		}
		snap.ParseFailures = failures
		snap.ParseFailuresTruncated = skip.ParseIncomplete+skip.ParseFailed > len(failures)
		var all []Tag
		for _, f := range files {
			all = append(all, f.tags...)
		}
		sortTags(all)
		// Preserve every file when symbols cannot represent the full scope.
		if opts.Offset == 0 && (skip != (SkipStats{}) || tagsOverflowBudget(all, snap.MaxBytes)) {
			snap.FilesParsed = len(files)
			snap.TotalDefs = len(all)
			assembleMapView(snap, candidates, snap.Path, opts.Depth, snap.MaxBytes)
			snap.Diagnostics = mapDiagnostics(snap, skip)
			return snap, nil
		}
		snap.FilesParsed = len(files)
		return tagsSnapshot(ctx, snap, []scannedFile{{tags: all}}, opts, snap.MaxBytes, skip), nil
	}

	assembleMapView(snap, candidates, snap.Path, opts.Depth, snap.MaxBytes)
	snap.Diagnostics = mapDiagnostics(snap, SkipStats{})
	return snap, nil
}

func inventoryEntryUnderScope(rel, scope string) bool {
	scope = strings.Trim(strings.TrimSpace(filepath.ToSlash(scope)), "/")
	return scope == "" || scope == "." || rel == scope || strings.HasPrefix(rel, scope+"/")
}

func readFileUnderRoot(root *os.Root, absRoot, path string) ([]byte, error) {
	rel, err := filepath.Rel(absRoot, path)
	if err != nil {
		return nil, err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("repomap: path escapes root")
	}
	return root.ReadFile(rel)
}
