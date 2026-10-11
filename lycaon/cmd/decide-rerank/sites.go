package main

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/repomap"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/summarize"
)

// Scope sizes keep one site call close to what the tool sees in practice.
const (
	summarizeScopeFiles = 80
	searchHitCap        = 32
	catalogIndexTimeout = 10 * time.Minute
)

// errSkip marks a pair the site could not exercise; the reason is the message.
type errSkip string

func (e errSkip) Error() string { return string(e) }

// siteDriver runs one site for one pair; the reranker's observer collects
// what the site offered and what came back.
type siteDriver interface {
	run(ctx context.Context, rr decide.Reranker, c *corpus, p pair) error
	close(ctx context.Context) error
}

func newSiteDriver(ctx context.Context, site decide.Site, c *corpus) (siteDriver, error) {
	switch site {
	case decide.SiteSummarizeStructure, decide.SiteSummarizeDefinitions:
		return summarizeDriver{}, nil
	case decide.SiteRepomapTags:
		return repomapDriver{}, nil
	case decide.SiteProjectSearch:
		return newSearchDriver(ctx, c)
	default:
		return nil, fmt.Errorf("site %s has no offline corpus; it is measured live", site)
	}
}

// summarizeDriver runs the summarize engine over the target's directory with
// a static gather built from the corpus, so structure and definitions rank
// exactly as the tool ranks them.
type summarizeDriver struct{}

type staticGather struct{ result summarize.GatherResult }

func (g staticGather) Gather(context.Context, summarize.Request) (summarize.GatherResult, error) {
	return g.result, nil
}

func (summarizeDriver) run(ctx context.Context, rr decide.Reranker, c *corpus, p pair) error {
	dir := path.Dir(p.File)
	caps := summarize.DefaultCaps()
	files := c.filesUnder(dir, p.File, summarizeScopeFiles)
	structure := make([]summarize.StructureCandidate, 0, len(files))
	for _, f := range files {
		src, err := c.read(f)
		if err != nil {
			continue
		}
		lines := strings.Split(string(src), "\n")
		sc := summarize.StructureCandidate{
			RelPath: f, Kind: summarize.StructureKindFile, StartLine: 1, LineCount: len(lines),
			Head: strings.Join(lines[:min(len(lines), caps.Gather.FileHeadLines)], "\n"),
		}
		for _, u := range c.byFile[f] {
			sc.Language = u.Lang
			sc.Symbols = append(sc.Symbols, summarize.StructureSymbol{
				Kind: u.Kind, Name: u.Symbol, Line: u.Line,
				Signature: summarize.SignatureLine(lines, u.Line), Doc: summarize.LeadingComment(lines, u.Line),
			})
		}
		structure = append(structure, sc)
	}
	if len(structure) < 2 {
		return errSkip("scope holds one file")
	}
	engine := summarize.NewEngine(staticGather{summarize.GatherResult{Mode: summarize.ModeRepo, Structure: structure}}, caps)
	engine.Rerank = rr
	_, err := engine.Run(ctx, summarize.Request{Task: p.Task, Path: dir})
	return err
}

func (summarizeDriver) close(context.Context) error { return nil }

// repomapDriver builds the tags view of the target's directory.
type repomapDriver struct{}

func (repomapDriver) run(ctx context.Context, rr decide.Reranker, c *corpus, p pair) error {
	snap, err := repomap.Build(ctx, repomap.Options{Root: c.root, Subpath: path.Dir(p.File), Task: p.Task, Rerank: rr})
	if err != nil {
		return err
	}
	if snap.View != "tags" {
		return errSkip("scope returned a " + snap.View + " view")
	}
	return nil
}

func (repomapDriver) close(context.Context) error { return nil }

// searchDriver runs the live code leg over an indexed copy of the corpus root.
type searchDriver struct {
	catalog *sourcecatalog.Catalog
	roots   []search.CodeRoot
}

func newSearchDriver(ctx context.Context, c *corpus) (*searchDriver, error) {
	provider, err := sourcescope.NewProvider(sourcescope.Config{}, func(context.Context, string) bool { return true })
	if err != nil {
		return nil, fmt.Errorf("source scope: %w", err)
	}
	catalog := sourcecatalog.Process()
	catalog.SetScopes(provider)
	root := sourcecatalog.Root{ID: "root", Path: c.root}
	if err := awaitIndex(ctx, catalog, c.name, root); err != nil {
		return nil, err
	}
	return &searchDriver{catalog: catalog, roots: []search.CodeRoot{{ProjectID: c.name, RootID: root.ID, Path: c.root}}}, nil
}

func awaitIndex(ctx context.Context, catalog *sourcecatalog.Catalog, projectID string, root sourcecatalog.Root) error {
	ctx, cancel := context.WithTimeout(ctx, catalogIndexTimeout)
	defer cancel()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		status, err := catalog.Trees.IndexStatus(ctx, projectID, root)
		if err != nil {
			return err
		}
		if status.Complete && !status.Refreshing && status.Error == "" {
			return nil
		}
		if status.State == sourcecatalog.StateFailed {
			return fmt.Errorf("index %s: %s", root.Path, status.Error)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func (d *searchDriver) run(ctx context.Context, rr decide.Reranker, _ *corpus, p pair) error {
	query := strings.TrimSpace(p.Query)
	if query == "" {
		return errSkip("pair carries no search query")
	}
	_, err := search.NewCodeExecutor(rr).Run(ctx, search.PlanLeg{
		Executor: search.ExecutorCode,
		Cap:      searchHitCap,
		Code:     &search.CodePlanLeg{Query: search.TextExpr{Text: query}, PathRoots: d.roots, Cap: searchHitCap, Lines: true},
	})
	return err
}

func (d *searchDriver) close(ctx context.Context) error {
	return d.catalog.Drain(ctx)
}

// candidateRef is the identity a site's candidate text carries.
type candidateRef struct {
	File   string
	Symbol string
	Line   int
}

// parseCandidate reads the identity every site writes at the head of its
// candidate text: "File: path (lang)", then "Symbol: name (kind)" or
// "Line N: text".
func parseCandidate(text string) candidateRef {
	var ref candidateRef
	head, rest, _ := strings.Cut(text, "\n")
	if file, ok := strings.CutPrefix(head, "File: "); ok {
		if i := strings.LastIndex(file, " ("); i > 0 && strings.HasSuffix(file, ")") {
			file = file[:i]
		}
		ref.File = file
	}
	second, _, _ := strings.Cut(rest, "\n")
	if symbol, ok := strings.CutPrefix(second, "Symbol: "); ok {
		if i := strings.LastIndex(symbol, " ("); i > 0 && strings.HasSuffix(symbol, ")") {
			symbol = symbol[:i]
		}
		ref.Symbol = symbol
	}
	if line, ok := strings.CutPrefix(second, "Line "); ok {
		if n, _, found := strings.Cut(line, ":"); found {
			ref.Line, _ = strconv.Atoi(n)
		}
	}
	return ref
}

// targetIndex finds the pair's unit among a site's candidates.
func targetIndex(site decide.Site, texts []string, p pair) int {
	for i, text := range texts {
		ref := parseCandidate(text)
		if ref.File != p.File {
			continue
		}
		switch site {
		case decide.SiteSummarizeStructure, decide.SiteProjectSearch:
			// A file-level site is right when the file is right: for search,
			// the first hit inside the unit's file is the answer a person opens.
			return i
		default:
			if ref.Symbol == p.Symbol {
				return i
			}
		}
	}
	return -1
}

var errNoObservation = errors.New("site produced no observation")
