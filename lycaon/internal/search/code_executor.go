package search

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

const (
	// Code scores remain below strong store relevance.
	codeScoreLineOnly = 0.35
	codeScorePathHit  = 0.45
	codeScoreBasename = 0.55
	codeScoreTermBump = 0.05
	codeScoreCap      = 0.65

	// Exact basenames outrank content and partial paths.
	fileScoreExactName = 0.95
	fileScoreBasename  = 0.6
	fileScorePath      = 0.5

	codeGenerationJoinGrace = 250 * time.Millisecond
)

// CodeExecutor runs the live code search leg over catalog generations.
type CodeExecutor struct {
	catalog *sourcecatalog.Catalog
	// rerank blends the decision engine into the leg's hits.
	rerank decide.Reranker
	// wallBudget overrides the budget's wall clock; zero uses the budget's.
	wallBudget time.Duration
	// generationJoinGrace overrides the cold-generation wait.
	generationJoinGrace time.Duration
}

func (e *CodeExecutor) legWallBudget(leg *CodePlanLeg) time.Duration {
	if e.wallBudget > 0 {
		return e.wallBudget
	}
	if leg.Wall > 0 {
		return leg.Wall
	}
	return leg.Budget.Wall()
}

func (e *CodeExecutor) generationWaitBudget() time.Duration {
	if e.generationJoinGrace > 0 {
		return e.generationJoinGrace
	}
	return codeGenerationJoinGrace
}

// NewCodeExecutor returns a CodeExecutor on the process catalog.
func NewCodeExecutor(rerank decide.Reranker) *CodeExecutor {
	return &CodeExecutor{catalog: sourcecatalog.Process(), rerank: rerank}
}

func (e *CodeExecutor) Source() string { return ExecutorCode }

// Run scans each root's generation under the leg's wall-clock budget. Cold
// generations get a short join before continuing in the background.
func (e *CodeExecutor) Run(ctx context.Context, leg PlanLeg) (ExecutorReport, error) {
	if leg.Code == nil {
		return ExecutorReport{}, fmt.Errorf("code leg missing")
	}
	if !leg.Code.Lines && !leg.Code.Files {
		return ExecutorReport{}, nil
	}
	if leg.Code.Query == nil {
		return ExecutorReport{}, fmt.Errorf("code query missing")
	}
	for _, root := range leg.Code.PathRoots {
		if strings.TrimSpace(root.RootID) == "" {
			return ExecutorReport{}, fmt.Errorf("code root ID is required")
		}
	}
	matcher, err := compileCodeQuery(leg.Code.Query, leg.Code.Flags)
	if err != nil {
		return ExecutorReport{}, err
	}
	paths, err := compileCodePaths(leg.Code.Query, leg.Code.Flags)
	if err != nil {
		return ExecutorReport{}, err
	}
	spec := codeScanSpec{
		matcher:      matcher,
		prefilter:    compileCodePrefilter(leg.Code.Query, leg.Code.Flags),
		wantLines:    leg.Code.Lines,
		wantFiles:    leg.Code.Files,
		lineCap:      legLineCap(leg.Code),
		fileCap:      legFileCap(leg.Code),
		fileExcludes: dependencyDirSet(leg.Code.FileExcludeDirs),
		lineExcludes: dependencyDirSet(leg.Code.LineExcludeDirs),
		terms:        queryTextTerms(leg.Code.Query),
		maxBytes:     codeExecutorMaxFileBytes,
	}
	legCtx, cancel := context.WithTimeout(ctx, e.legWallBudget(leg.Code))
	defer cancel()

	report := ExecutorReport{}
	stats := &report.Code
	for _, root := range leg.Code.PathRoots {
		if spec.lineCap <= 0 && spec.fileCap <= 0 {
			report.Limited = true
			break
		}
		started := time.Now()
		gen, err := resolveCodeGeneration(legCtx, e.catalog, root, e.generationWaitBudget())
		stats.GenerationWait += time.Since(started)
		if errors.Is(err, errCodeCatalogWarming) {
			stats.WarmingRoots++
			continue
		}
		if err == nil {
			err = e.scanGeneration(legCtx, gen, paths, &spec, &report)
		}
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		if legCtx.Err() != nil {
			report.TimedOut = true
			break
		}
		if err != nil {
			report.Issues = append(report.Issues, Issue{Executor: ExecutorCode, Reason: IssueExecutorError, Message: err.Error()})
		}
	}
	e.rerankHits(ctx, spec.terms, report.Hits)
	return report, nil
}

// rerankHits blends engine relevance into the leg's hit scores on the
// project_search site. The leg's own scale stays, so a code hit still sits
// below a strong store hit; the engine reorders within the leg.
func (e *CodeExecutor) rerankHits(ctx context.Context, terms []string, hits []Hit) {
	if len(hits) < 2 || !e.rerank.Active(decide.SiteProjectSearch) {
		return
	}
	task := strings.Join(searchTaskTerms(terms), " ")
	lexical := make([]float64, len(hits))
	texts := make([]string, len(hits))
	for i, hit := range hits {
		lexical[i] = hit.Score
		texts[i] = hitText(hit)
	}
	blended, _ := e.rerank.Rerank(ctx, decide.SiteProjectSearch, task, lexical, texts)
	for i := range hits {
		hits[i].Score = blended[i]
	}
}

// searchTaskTerms drops the wildcard placeholder from the query terms.
func searchTaskTerms(terms []string) []string {
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		if term != ".+" && strings.TrimSpace(term) != "" {
			out = append(out, term)
		}
	}
	return out
}

// hitText is what the engine reads for one code leg hit.
func hitText(hit Hit) string {
	if hit.HitKind == HitKindFile || hit.Line <= 0 {
		return "File: " + hit.Path
	}
	return "File: " + hit.Path + "\nLine " + fmt.Sprint(hit.Line) + ": " + hit.Snippet
}

func legLineCap(leg *CodePlanLeg) int {
	if !leg.Lines {
		return 0
	}
	if leg.Cap <= 0 {
		return SearchExecutorProbeHits
	}
	return leg.Cap
}

func legFileCap(leg *CodePlanLeg) int {
	if !leg.Files {
		return 0
	}
	if leg.FileCap <= 0 {
		return SearchExecutorProbeHits
	}
	return leg.FileCap
}

// names are single path segments; prefixes are multi-segment relative paths.
type dependencyDirs struct {
	names    map[string]struct{}
	prefixes []string
}

func dependencyDirSet(patterns []string) dependencyDirs {
	if len(patterns) == 0 {
		return dependencyDirs{}
	}
	out := dependencyDirs{names: make(map[string]struct{}, len(patterns))}
	for _, p := range patterns {
		name := strings.Trim(strings.TrimSpace(p), "/")
		if name == "" || strings.ContainsAny(name, "*?") {
			continue
		}
		if strings.Contains(name, "/") {
			out.prefixes = append(out.prefixes, name)
			continue
		}
		out.names[name] = struct{}{}
	}
	return out
}

func (d dependencyDirs) empty() bool {
	return len(d.names) == 0 && len(d.prefixes) == 0
}

// underDependencyDir applies an explicit per-arm dependency/build-tree exclusion.
func underDependencyDir(rel string, excludes dependencyDirs) bool {
	_, under := dependencyDirOf(rel, excludes)
	return under
}

// dependencyDirOf returns the shallowest excluded directory holding rel.
func dependencyDirOf(rel string, excludes dependencyDirs) (string, bool) {
	if excludes.empty() {
		return "", false
	}
	dir, found := "", false
	for start := 0; start <= len(rel); {
		end := strings.IndexByte(rel[start:], '/')
		if end < 0 {
			end = len(rel)
		} else {
			end += start
		}
		if _, skip := excludes.names[rel[start:end]]; skip {
			dir, found = rel[:end], true
			break
		}
		start = end + 1
	}
	for _, prefix := range excludes.prefixes {
		at := -1
		if strings.HasPrefix(rel, prefix+"/") {
			at = 0
		} else if i := strings.Index(rel, "/"+prefix+"/"); i >= 0 {
			at = i + 1
		}
		if at >= 0 && (!found || at+len(prefix) < len(dir)) {
			dir, found = rel[:at+len(prefix)], true
		}
	}
	return dir, found
}

func fileHitScore(rel string, terms []string) float64 {
	base := strings.ToLower(filepath.Base(rel))
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	joined := strings.ToLower(strings.Join(terms, " "))
	if joined == base || joined == stem {
		return fileScoreExactName
	}
	if strings.Contains(base, joined) {
		return fileScoreBasename
	}
	for _, term := range terms {
		if strings.Contains(base, strings.ToLower(term)) {
			return fileScoreBasename
		}
	}
	return fileScorePath
}

func codeHitScore(relPath, line string, terms []string) float64 {
	score := codeScoreLineOnly
	lowerBase := strings.ToLower(filepath.Base(relPath))
	lowerPath := strings.ToLower(relPath)
	lowerLine := strings.ToLower(line)
	termHits := 0
	for _, term := range terms {
		if term == ".+" {
			continue
		}
		t := strings.ToLower(term)
		if t == "" {
			continue
		}
		if strings.Contains(lowerBase, t) {
			if score < codeScoreBasename {
				score = codeScoreBasename
			}
		} else if strings.Contains(lowerPath, t) {
			if score < codeScorePathHit {
				score = codeScorePathHit
			}
		}
		if strings.Contains(lowerLine, t) {
			termHits++
		}
	}
	if termHits > 1 {
		score += codeScoreTermBump * float64(termHits-1)
	}
	if score > codeScoreCap {
		score = codeScoreCap
	}
	return score
}

func codeSearchTerms(pattern string) []string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || pattern == "*" {
		return []string{".+"}
	}
	if strings.ContainsAny(pattern, " \t") {
		return strings.Fields(pattern)
	}
	return []string{pattern}
}
