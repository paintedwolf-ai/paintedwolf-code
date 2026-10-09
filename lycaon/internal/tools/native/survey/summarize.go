package survey

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/lycaon/lycaon/pkg/api"
)

// SummarizeTool assembles a bounded briefing pack.
type SummarizeTool struct {
	Observe  func(summarize.Result)
	Boundary *sandbox.Boundary
	Caps     summarize.Caps
	Catalog  *sourcecatalog.Catalog
	// Rerank blends the decision engine into the pack's task-ranked lists.
	Rerank decide.Reranker
}

type summarizeAnchor struct {
	Handle  string `json:"handle,omitempty"`
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Excerpt string `json:"excerpt"`
	// Rank is the anchor's relevance order.
	Rank int `json:"rank,omitempty"`
}

// summarizePack is the wire form of summarize.ContextPack.
type summarizePack struct {
	Identity  []summarizePackIdentity   `json:"identity,omitempty"`
	Skeleton  []summarizePackSymbol     `json:"skeleton,omitempty"`
	Substance []summarizePackWindow     `json:"substance,omitempty"`
	Imports   []summarizePackImportEdge `json:"imports,omitempty"`
	CallSites []summarizePackCallSite   `json:"call_sites,omitempty"`
	Neighbors []summarizePackNeighbor   `json:"neighbors,omitempty"`
}

type summarizePackIdentity struct {
	Path          string `json:"path"`
	Kind          string `json:"kind"`
	LineCount     int    `json:"line_count,omitempty"`
	Language      string `json:"language,omitempty"`
	OutlineSource string `json:"outline_source,omitempty"`
	ImportPath    string `json:"import_path,omitempty"`
	LogDigest     string `json:"log_digest,omitempty"`
}

type summarizePackSymbol struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	Line int    `json:"line,omitempty"`
}

type summarizePackWindow struct {
	Handle    string `json:"handle,omitempty"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Symbol    string `json:"symbol,omitempty"`
	Body      string `json:"body"`
}

type summarizePackImportEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind,omitempty"`
}

type summarizePackCallSite struct {
	Path    string `json:"path"`
	Line    int    `json:"line,omitempty"`
	Excerpt string `json:"excerpt"`
}

type summarizePackNeighbor struct {
	Path string `json:"path"`
	Why  string `json:"why,omitempty"`
}

type summarizeGatherReport struct {
	Mode          string                        `json:"mode"`
	Path          string                        `json:"path,omitempty"`
	Paths         []string                      `json:"paths,omitempty"`
	Pattern       string                        `json:"pattern,omitempty"`
	Candidates    int                           `json:"candidates"`
	Bytes         int                           `json:"bytes"`
	MatchCount    int                           `json:"match_count,omitempty"`
	SampleMatches []summarizePatternMatchSample `json:"sample_matches,omitempty"`
}

type summarizePatternMatchSample struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

// maxSourcesTouched bounds the wire source list.
const maxSourcesTouched = 64

type summarizeResponse struct {
	Task           string                 `json:"task"`
	Coverage       api.SummarizeCoverage  `json:"coverage"`
	Pack           summarizePack          `json:"pack"`
	Anchors        []summarizeAnchor      `json:"anchors,omitempty"`
	NextActions    []summarize.NextAction `json:"next_actions,omitempty"`
	Gather         summarizeGatherReport  `json:"gather"`
	SourcesTouched []string               `json:"sources_touched,omitempty"`
	// Rerank says what the local engine ranked inside this call; absent when it answered nothing.
	Rerank *decide.RerankReceipt `json:"rerank,omitempty"`
}

func (t *SummarizeTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if t == nil || t.Boundary == nil {
		return "", fmt.Errorf("summarize: not configured")
	}

	// Material is required; task only ranks it.
	content := summarizeStringArg(args, "content")
	pathArg := summarizeStringArg(args, "path")
	pattern := summarizeStringArg(args, "pattern")
	cursor := summarizeStringArg(args, "cursor")
	paths := summarizeStringsArg(args, "paths")
	// Promote an unambiguous comma-separated path list.
	if pathArg != "" && len(paths) == 0 {
		if split := splitCommaSeparatedPaths(pathArg); len(split) > 1 {
			paths = split
			pathArg = ""
		}
	}
	if pathArg != "" && len(paths) > 0 {
		paths = append([]string{pathArg}, paths...)
		pathArg = ""
	}
	task := summarize.DefaultTask(summarizeStringArg(args, "task"), pathArg, paths, content)
	hasRepo := pathArg != "" || len(paths) > 0
	if hasRepo {
		var err error
		ctx, err = t.Boundary.WithToolProfileSnapshot(ctx, tctx.ProfileID())
		if err != nil {
			return "", err
		}
	}

	if pattern != "" && pathArg == "" && len(paths) == 0 {
		return "", &toolrejection.ToolReject{
			Code: "SUMMARIZE_PATTERN_NEEDS_PATH",
			Data: map[string]any{},
		}
	}
	if content == "" && !hasRepo {
		return "", &toolrejection.ToolReject{
			Code: "SUMMARIZE_NO_INPUT",
			Data: map[string]any{
				"need_one_of": []string{"path", "paths", "content"},
				"hint":        `call summarize with a path, e.g. {"path":"README.md"} — task is optional`,
			},
		}
	}
	if content != "" {
		n := len(content)
		if n > t.Caps.Gather.InlineMaxBytes {
			return "", &toolrejection.ToolReject{Code: "SUMMARIZE_CONTENT_TOO_LARGE", Data: map[string]any{"bytes": n, "cap": t.Caps.Gather.InlineMaxBytes}}
		}
		// Preserve short unstructured content verbatim.
		if n < t.Caps.Gather.InlineMinBytes && !hasRepo {
			outline := fileoutline.AnalyzeText(ctx, inlineOutlineHint(content), []byte(content))
			if !inlineOutlineUsable(outline) {
				resp := buildSummarizeResponse(inlinePassthroughResult(task, content))
				if outline.ParseFailure != nil {
					observability.LogSummarizeCall(observability.SummarizeDebugCapture{Tool: "summarize", Task: task, Mode: "inline", Diagnostics: []tsparse.FileFailure{{Path: "inline", Phase: "source", Failure: outline.ParseFailure}}})
				}
				raw, err := surveyjson.Marshal(resp)
				if err != nil {
					return "", fmt.Errorf("summarize encode: %w", err)
				}
				return string(raw), nil
			}
		}
	}
	req := summarize.Request{
		Task:       task,
		Content:    content,
		Path:       pathArg,
		Paths:      paths,
		Pattern:    pattern,
		Cursor:     cursor,
		MaxAnchors: t.Caps.ClampAnchors(0), // host anchors.default — not an agent arg
	}

	rerankLedger := decide.NewRerankLedger()
	rerank := t.Rerank.WithLedger(rerankLedger)
	reads := projectpaths.NewReadSession(t.Boundary, tctx)
	defer reads.Close()
	gatherer := &summarizeGatherer{boundary: t.Boundary, reads: reads, caps: t.Caps, tctx: tctx, catalog: t.Catalog, rerank: rerank}
	defer gatherer.closeTrees()
	engine := summarize.NewEngine(gatherer, t.Caps)
	engine.Outliner = gatherer
	engine.Rerank = rerank
	// Reserve room for the complete response envelope.
	engine.WireFit = func(r summarize.Result) int {
		gatherer.stampWork(&r)
		raw, err := surveyjson.Marshal(buildSummarizeResponse(r))
		if err != nil {
			return 0
		}
		// Evidence commit adds one handle to each anchor and its source window.
		return t.Caps.EstimateTokens(string(raw)) + t.Caps.EstimateTokens(strings.Repeat("x", 96*len(r.Anchors)))
	}
	if t.Caps.Pack.WireBudgetTokens > 0 {
		engine.WireEnvelopeTokens = estimateWireEnvelopeTokens(t.Caps)
	}

	res, err := engine.Run(ctx, req)
	if err == nil && gatherer.treeErr != nil {
		err = gatherer.treeErr
	}
	if err != nil {
		if errors.Is(err, summarize.ErrNoMaterial) {
			return "", &toolrejection.ToolReject{Code: "SUMMARIZE_NO_MATERIAL", Data: gatherer.noMaterialData(req)}
		}
		if errors.Is(err, summarize.ErrInvalidCursor) || errors.Is(err, sourcecatalog.ErrTreeCursor) {
			return "", &toolrejection.ToolReject{Code: "SUMMARIZE_CURSOR_STALE", Data: map[string]any{"cursor": cursor}}
		}
		return "", err
	}

	res.NextActions = validateNextActions(ctx, t.Boundary, tctx, gatherer.actionableSources(res.NextActions))
	gatherer.stampWork(&res)
	if t.Observe != nil {
		t.Observe(res)
	}
	observability.LogSummarizeCall(observability.SummarizeDebugCapture{
		Tool: "summarize", Task: res.Task, Mode: string(res.Gather.Mode), Work: res.Orchestration,
		Diagnostics: map[string]any{"sources": res.Pack.Identity, "parse_failures": gatherer.parseFailures},
	})

	resp := buildSummarizeResponse(res)
	resp.Rerank = rerankLedger.Receipt()
	raw, err := surveyjson.Marshal(resp)
	if err != nil {
		return "", fmt.Errorf("summarize encode: %w", err)
	}
	return string(raw), nil
}

// inlinePassthroughResult preserves short unstructured content.
func inlinePassthroughResult(task, content string) summarize.Result {
	return summarize.Result{
		Task: task,
		Pack: summarize.ContextPack{
			Identity: []summarize.PackIdentity{{
				Path: "inline", Kind: "inline",
				LineCount: strings.Count(content, "\n") + 1, ParseHealth: "ok",
			}},
			Substance: []summarize.PackWindow{{
				Path: "inline", StartLine: 1, Body: content,
			}},
		},
		Coverage: summarize.Coverage{Complete: true},
		Sources:  []string{"inline"},
		Gather: summarize.GatherReport{
			Mode:       summarize.ModeInline,
			Candidates: 1,
			Bytes:      len(content),
		},
	}
}

// inlineOutlineUsable reports whether inline content has parseable structure.
func inlineOutlineUsable(outline fileoutline.Result) bool {
	if len(outline.Symbols) > 0 {
		return true
	}
	return outline.Parses != nil && *outline.Parses
}

// inlineOutlineHint maps structural prefixes to parser hints.
func inlineOutlineHint(content string) string {
	trim := strings.TrimSpace(content)
	if trim == "" {
		return ""
	}
	switch trim[0] {
	case '{', '[':
		return "snippet.json"
	case '-':
		return "snippet.yaml"
	}
	first := trim
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	first = strings.TrimSpace(first)
	switch {
	case strings.HasPrefix(first, "package "), strings.HasPrefix(first, "func "),
		strings.HasPrefix(first, "type "), strings.HasPrefix(first, "import "):
		return "snippet.go"
	case strings.HasPrefix(first, "fn "), strings.HasPrefix(first, "pub "),
		strings.HasPrefix(first, "mod "), strings.HasPrefix(first, "use "):
		return "snippet.rs"
	case strings.HasPrefix(first, "export "), strings.HasPrefix(first, "const "),
		strings.HasPrefix(first, "function "), strings.HasPrefix(first, "class "),
		strings.HasPrefix(first, "interface "):
		return "snippet.ts"
	case strings.HasPrefix(first, "def "), strings.HasPrefix(first, pyAsyncDefPrefix):
		return "snippet.py"
	}
	return ""
}

const pyAsyncDefPrefix = "async def "

func buildSummarizeResponse(res summarize.Result) summarizeResponse {
	resp := summarizeResponse{
		Task:           res.Task,
		Coverage:       coverageWire(res.Coverage),
		Pack:           packWire(res.Pack),
		NextActions:    res.NextActions,
		SourcesTouched: capSourcesTouched(res.Sources),
		Gather: summarizeGatherReport{
			Mode:          string(res.Gather.Mode),
			Path:          res.Gather.Path,
			Paths:         res.Gather.Paths,
			Pattern:       res.Gather.Pattern,
			Candidates:    res.Gather.Candidates,
			Bytes:         res.Gather.Bytes,
			MatchCount:    res.Gather.MatchCount,
			SampleMatches: patternMatchSamplesWire(res.Gather.SampleMatches),
		},
	}
	for i, a := range res.Anchors {
		resp.Anchors = append(resp.Anchors, summarizeAnchor{
			Path: a.Path, Line: a.Line, Excerpt: a.Excerpt,
			Rank: i + 1,
		})
	}
	return resp
}

func coverageWire(coverage summarize.Coverage) api.SummarizeCoverage {
	return api.SummarizeCoverage{
		CatalogState: coverage.CatalogState, CatalogRefreshing: coverage.CatalogRefreshing,
		Complete: coverage.Complete, CatalogRevision: coverage.CatalogRevision,
		FilesTotal: coverage.FilesTotal, FilesRepresented: coverage.FilesRepresented,
		DefinitionsTotal: coverage.DefinitionsTotal, AnchorsReturned: coverage.AnchorsReturned,
		MatchingFilesObserved: coverage.MatchingFilesObserved, MatchesObserved: coverage.MatchesObserved, MatchSamplesReturned: coverage.MatchSamplesReturned,
		ChildrenTotal: coverage.ChildrenTotal, ChildrenReturned: coverage.ChildrenReturned,
		Cursor: coverage.Cursor, NextCursor: coverage.NextCursor,
	}
}

// estimateWireEnvelopeTokens reserves space outside the pack.
func estimateWireEnvelopeTokens(caps summarize.Caps) int {
	stub := summarize.Result{
		Task: "x",
	}
	raw, err := surveyjson.Marshal(buildSummarizeResponse(stub))
	if err != nil {
		return 0
	}
	return caps.EstimateTokens(string(raw))
}

// packWire exposes source material; parser diagnostics stay host-side.
func packWire(pack summarize.ContextPack) summarizePack {
	out := summarizePack{}
	for _, id := range pack.Identity {
		out.Identity = append(out.Identity, summarizePackIdentity{
			Path: id.Path, Kind: id.Kind, LineCount: id.LineCount,
			Language:      id.Language,
			OutlineSource: id.OutlineSource, ImportPath: id.ImportPath,
			LogDigest: id.LogDigest,
		})
	}
	for _, s := range pack.Skeleton {
		out.Skeleton = append(out.Skeleton, summarizePackSymbol{
			Path: s.Path, Kind: s.Kind, Name: s.Name, Line: s.Line,
		})
	}
	for _, w := range pack.Substance {
		out.Substance = append(out.Substance, summarizePackWindow{
			Path: w.Path, StartLine: w.StartLine, EndLine: w.EndLine,
			Symbol: w.Symbol, Body: w.Body,
		})
	}
	for _, e := range pack.Imports {
		out.Imports = append(out.Imports, summarizePackImportEdge{From: e.From, To: e.To, Kind: e.Kind})
	}
	for _, c := range pack.CallSites {
		out.CallSites = append(out.CallSites, summarizePackCallSite{Path: c.Path, Line: c.Line, Excerpt: c.Excerpt})
	}
	for _, n := range pack.Neighbors {
		out.Neighbors = append(out.Neighbors, summarizePackNeighbor{Path: n.Path, Why: n.Why})
	}
	return out
}

func capSourcesTouched(sources []string) []string {
	if len(sources) <= maxSourcesTouched {
		return sources
	}
	return sources[:maxSourcesTouched]
}

func patternMatchSamplesWire(in []summarize.PatternMatchSample) []summarizePatternMatchSample {
	if len(in) == 0 {
		return nil
	}
	out := make([]summarizePatternMatchSample, len(in))
	for i, s := range in {
		out[i] = summarizePatternMatchSample{Path: s.Path, Line: s.Line, Content: s.Content}
	}
	return out
}

// validateNextActions keeps resolvable routing payloads.
func validateNextActions(ctx context.Context, boundary *sandbox.Boundary, tctx tools.ToolContext, in []summarize.NextAction) []summarize.NextAction {
	if len(in) == 0 {
		return nil
	}
	out := make([]summarize.NextAction, 0, len(in))
	for _, na := range in {
		if keepNextAction(ctx, boundary, tctx, na) {
			out = append(out, na)
		}
	}
	return out
}

func keepNextAction(ctx context.Context, boundary *sandbox.Boundary, tctx tools.ToolContext, na summarize.NextAction) bool {
	tool := strings.TrimSpace(na.Tool)
	if tool == "" {
		return false
	}
	switch tool {
	case "read", "find", "list_dir", "summarize":
		if tool == "summarize" && len(na.Paths) > 0 {
			for _, path := range na.Paths {
				resolved, err := projectpaths.ResolveRead(ctx, boundary, tctx, path)
				if err != nil {
					return false
				}
				if _, err = os.Stat(resolved.Abs); err != nil {
					return false
				}
			}
			return true
		}
		path := strings.TrimSpace(na.Path)
		if path == "" {
			return false
		}
		resolved, err := projectpaths.ResolveRead(ctx, boundary, tctx, path)
		if err != nil {
			return false
		}
		_, err = os.Stat(resolved.Abs)
		return err == nil
	case "grep":
		pattern := strings.TrimSpace(na.Pattern)
		if pattern == "" {
			return false
		}
		return grepPatternCompiles(pattern)
	default:
		return false
	}
}

func grepPatternCompiles(pattern string) bool {
	_, err := regexp.Compile(pattern)
	return err == nil
}

func summarizeStringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

func summarizeStringsArg(args map[string]any, key string) []string {
	if args == nil {
		return nil
	}
	switch v := args[key].(type) {
	case []string:
		return trimNonEmpty(v)
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return trimNonEmpty(out)
	}
	return nil
}

func trimNonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// splitCommaSeparatedPaths recognizes path-like comma-separated segments.
func splitCommaSeparatedPaths(pathArg string) []string {
	if !strings.Contains(pathArg, ",") {
		return nil
	}
	raw := strings.Split(pathArg, ",")
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil
		}
		if !pathLikeToken(p) {
			return nil
		}
		out = append(out, p)
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

func pathLikeToken(p string) bool {
	if strings.Contains(p, "/") || strings.Contains(p, `\`) {
		return true
	}
	return strings.Contains(p, ".")
}
