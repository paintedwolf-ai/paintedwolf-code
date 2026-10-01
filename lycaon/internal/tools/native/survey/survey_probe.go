package survey

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

const literalLineCaptureBytes = 4 << 10

// ProbeGrepRecords scans one survey pattern.
func ProbeGrepRecords(ctx context.Context, boundary *sandbox.Boundary, catalog *sourcecatalog.Catalog, tctx tools.ToolContext, relPath, pattern, label string, maxMatches int) ([]evidence.Record, int, SurveyGrepBatchStats, error) {
	batch, stats, err := ProbeGrepBatchRecords(ctx, boundary, catalog, tctx, relPath, []SurveyGrepSpec{{
		Label: label, Pattern: pattern,
	}}, maxMatches)
	if err != nil {
		return nil, 0, stats, err
	}
	return batch[0].Records, batch[0].MatchCount, stats, nil
}

// ProbeFindRecords scans one survey glob.
func ProbeFindRecords(ctx context.Context, boundary *sandbox.Boundary, catalog *sourcecatalog.Catalog, tctx tools.ToolContext, relPath, nameGlob, label string, maxResults int) ([]evidence.Record, int, error) {
	results, total, err := probeFind(ctx, boundary, catalog, tctx, relPath, nameGlob, label, maxResults)
	if err != nil {
		return nil, 0, err
	}
	return findProbeRecords(label, results), total, nil
}

// scanLiteralMatches verifies every indexed candidate.
func scanLiteralMatches(
	ctx context.Context,
	boundary *sandbox.Boundary,
	catalog *sourcecatalog.Catalog,
	tctx tools.ToolContext,
	relPath string,
	literals []string,
	walkExtra sandbox.SurveyOptions,
	visit func(grepMatch),
) error {
	if sandbox.HasParentTraversal(relPath) {
		return toolkit.PathEscapeReject(relPath)
	}
	literals = literalSearchTerms(literals)
	if len(literals) == 0 {
		return nil
	}
	automaton := newLiteralAutomaton(literals)
	reads := projectpaths.NewReadSession(boundary, tctx)
	defer reads.Close()
	tool := &GrepTool{Boundary: boundary, Catalog: catalog}
	_, targets, err := tool.resolveGrepTargets(ctx, tctx, map[string]any{"path": relPath})
	if err != nil {
		return err
	}
	for _, target := range targets {
		_, statErr := os.Stat(target.fullRoot)
		if statErr != nil {
			return grepRootStatErr(target.displayRoot, statErr)
		}
		readScope, filterErr := boundary.CompileReadScope(ctx, target.root.Path, tctx.ProfileID())
		if filterErr != nil {
			return filterErr
		}
		inventory, err := sourceInventoryForScope(ctx, catalog, tctx.ProjectID, target.root, target.fullRoot)
		if err != nil {
			return err
		}
		scan := literalCatalogScan{
			reads: reads, tctx: tctx, root: target.root, readFilter: readScope.Filter,
			catalog: catalogOrProcess(catalog), drafts: sourceview.DraftsFor(ctx, tctx),
			require: litprefilter.AnyOf(literals...), automaton: automaton, visit: visit,
		}
		var scanErr error
		walkErr := inventory.walk(ctx, func(entry sourcecatalog.Entry) sourcecatalog.WalkStep {
			if entry.IsDir {
				if scan.readFilter != nil && !scan.readFilter(entry.Path, true) {
					return sourcecatalog.WalkSkip
				}
				if walkExtra.PruneNestedVCS && entry.IsVCSRoot {
					if walkExtra.OnNestedRepoPruned != nil {
						walkExtra.OnNestedRepoPruned(filepath.Join(target.root.Path, filepath.FromSlash(entry.Path)))
					}
					return sourcecatalog.WalkSkip
				}
				return sourcecatalog.WalkContinue
			}
			if entry.IsSymlink {
				return sourcecatalog.WalkContinue
			}
			if scanErr = scan.file(ctx, entry); scanErr != nil {
				return sourcecatalog.WalkStop
			}
			return sourcecatalog.WalkContinue
		})
		if scanErr != nil {
			return scanErr
		}
		if walkErr != nil {
			return walkErr
		}
	}
	return nil
}

// literalCatalogScan verifies one root's cataloged files against the literals.
type literalCatalogScan struct {
	reads      *projectpaths.ReadSession
	tctx       tools.ToolContext
	root       projectroot.RootRef
	readFilter sandbox.ReadFilter
	catalog    *sourcecatalog.Catalog
	drafts     sourceview.DraftOverlay
	require    litprefilter.Requirement
	automaton  *literalAutomaton
	visit      func(grepMatch)
}

// file scans one entry unless the literal index proves every literal absent.
func (s literalCatalogScan) file(ctx context.Context, entry sourcecatalog.Entry) error {
	abs := filepath.Join(s.root.Path, filepath.FromSlash(entry.Path))
	if s.readFilter != nil && !s.readFilter(entry.Path, false) {
		return nil
	}
	if _, hasDraft := s.drafts.Lookup(abs); !hasDraft && s.catalog != nil && s.catalog.CanPrune(s.root.Path, s.require, entry) {
		return nil
	}
	fileInfo, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) || os.IsPermission(err) {
			return nil
		}
		return err
	}
	if fileInfo.IsDir() || fileInfo.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	rel := projectpaths.QualifyAbs(s.tctx, s.root, abs)
	if err := scanLiteralFileWithAutomaton(ctx, s.reads, abs, rel, s.automaton, s.visit); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if os.IsNotExist(err) || os.IsPermission(err) {
			return nil
		}
		return err
	}
	return nil
}

func scanLiteralFileWithAutomaton(
	ctx context.Context,
	reads *projectpaths.ReadSession,
	absPath, displayPath string,
	automaton *literalAutomaton,
	visit func(grepMatch),
) error {
	resolved, err := reads.Resolve(ctx, absPath)
	if err != nil {
		return err
	}
	f, err := reads.Open(resolved)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	return scanLiteralLines(ctx, summaryDecodedReader(f), displayPath, automaton, visit)
}

func scanLiteralLines(ctx context.Context, reader io.Reader, displayPath string, automaton *literalAutomaton, visit func(grepMatch)) error {
	line := newLiteralLineMatcher(automaton)
	validator := textfile.UTF8Validator{}
	buffered := bufio.NewReaderSize(reader, 64*1024)
	lineNumber := 1
	for {
		fragment, err := buffered.ReadSlice('\n')
		if !validator.Add(fragment, errors.Is(err, io.EOF)) {
			return nil
		}
		endsLine := len(fragment) > 0 && fragment[len(fragment)-1] == '\n'
		if endsLine {
			fragment = fragment[:len(fragment)-1]
		}
		line.add(fragment)
		if endsLine || errors.Is(err, io.EOF) {
			line.finish(displayPath, lineNumber)
			line.reset()
			lineNumber++
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	for _, match := range line.results() {
		if visit != nil {
			visit(match)
		}
	}
	return nil
}

type literalAutomatonNode struct {
	next    map[byte]int
	fail    int
	outputs []int
}

type literalAutomaton struct {
	terms []string
	nodes []literalAutomatonNode
}

func newLiteralAutomaton(terms []string) *literalAutomaton {
	a := &literalAutomaton{terms: terms, nodes: []literalAutomatonNode{{next: map[byte]int{}}}}
	for termIndex, term := range terms {
		state := 0
		for _, b := range []byte(term) {
			next, ok := a.nodes[state].next[b]
			if !ok {
				next = len(a.nodes)
				a.nodes[state].next[b] = next
				a.nodes = append(a.nodes, literalAutomatonNode{next: map[byte]int{}})
			}
			state = next
		}
		a.nodes[state].outputs = append(a.nodes[state].outputs, termIndex)
	}
	queue := make([]int, 0, len(a.nodes))
	for _, child := range a.nodes[0].next {
		queue = append(queue, child)
	}
	for len(queue) > 0 {
		state := queue[0]
		queue = queue[1:]
		for b, child := range a.nodes[state].next {
			queue = append(queue, child)
			fail := a.nodes[state].fail
			for fail != 0 {
				if next, ok := a.nodes[fail].next[b]; ok {
					fail = next
					break
				}
				fail = a.nodes[fail].fail
			}
			if fail == 0 {
				if next, ok := a.nodes[0].next[b]; ok && next != child {
					fail = next
				}
			}
			a.nodes[child].fail = fail
			a.nodes[child].outputs = append(a.nodes[child].outputs, a.nodes[fail].outputs...)
		}
	}
	return a
}

func (a *literalAutomaton) step(state int, b byte) int {
	for state != 0 {
		if next, ok := a.nodes[state].next[b]; ok {
			return next
		}
		state = a.nodes[state].fail
	}
	if next, ok := a.nodes[0].next[b]; ok {
		return next
	}
	return 0
}

type literalLineMatcher struct {
	automaton *literalAutomaton
	state     int
	capture   []byte
	lineHits  map[int]struct{}
	counts    []int
	first     []grepMatch
}

func newLiteralLineMatcher(automaton *literalAutomaton) *literalLineMatcher {
	return &literalLineMatcher{
		automaton: automaton,
		lineHits:  make(map[int]struct{}),
		counts:    make([]int, len(automaton.terms)),
		first:     make([]grepMatch, len(automaton.terms)),
	}
}

func (l *literalLineMatcher) add(fragment []byte) {
	if len(l.capture) < literalLineCaptureBytes {
		n := min(len(fragment), literalLineCaptureBytes-len(l.capture))
		l.capture = append(l.capture, fragment[:n]...)
	}
	for _, b := range fragment {
		l.state = l.automaton.step(l.state, b)
		for _, term := range l.automaton.nodes[l.state].outputs {
			l.lineHits[term] = struct{}{}
		}
	}
}

func (l *literalLineMatcher) finish(displayPath string, lineNumber int) {
	content := textfile.TrimIncompleteTail(l.capture)
	if len(content) > 0 && content[len(content)-1] == '\r' {
		content = content[:len(content)-1]
	}
	for term := range l.lineHits {
		l.counts[term]++
		if l.first[term].Path == "" {
			l.first[term] = grepMatch{
				Path: displayPath, Line: lineNumber, Content: string(content), Match: l.automaton.terms[term],
			}
		}
	}
}

func (l *literalLineMatcher) reset() {
	l.state = 0
	l.capture = l.capture[:0]
	clear(l.lineHits)
}

func (l *literalLineMatcher) results() []grepMatch {
	out := make([]grepMatch, 0, len(l.first))
	for i, match := range l.first {
		if match.Path == "" {
			continue
		}
		match.Count = l.counts[i]
		out = append(out, match)
	}
	return out
}

func literalSearchTerms(literals []string) []string {
	out := make([]string, 0, len(literals))
	seen := make(map[string]struct{}, len(literals))
	for _, literal := range literals {
		if literal == "" || strings.ContainsAny(literal, "\r\n") {
			continue
		}
		if _, ok := seen[literal]; ok {
			continue
		}
		seen[literal] = struct{}{}
		out = append(out, literal)
	}
	sort.Strings(out)
	return out
}

func probeFind(ctx context.Context, boundary *sandbox.Boundary, catalog *sourcecatalog.Catalog, tctx tools.ToolContext, relPath, nameGlob, label string, maxResults int) ([]findResult, int, error) {
	if sandbox.HasParentTraversal(relPath) {
		return nil, 0, toolkit.PathEscapeReject(relPath)
	}
	tool := &FindTool{Boundary: boundary, Catalog: catalog}
	roots, err := projectpaths.UnionDiscoveryRoots(ctx, tctx, relPath)
	if err != nil {
		return nil, 0, err
	}
	sampler := findResultSampler{capacity: maxResults, seed: surveySampleSeed(label, nameGlob)}
	resp := findResponse{Results: []findResult{}}
	skipped := 0
	matchedTotal := 0
	truncated := false
	deeperPathsOmitted := false
	entryType := findTypeFile

	if projectroot.IsUnionDiscoveryPath(relPath) {
		for _, root := range roots {
			if err := tool.runFindRoot(ctx, tctx, root, root.Path, projectpaths.QualifyAbs(tctx, root, root.Path),
				findDepthUnbounded, maxResults, 0, nameGlob, entryType, sampler.add, &resp,
				&skipped, &matchedTotal, &truncated, &deeperPathsOmitted); err != nil {
				return nil, 0, err
			}
		}
	} else {
		resolved, err := projectpaths.ResolveRead(ctx, boundary, tctx, relPath)
		if err != nil {
			return nil, 0, err
		}
		if err := tool.runFindRoot(ctx, tctx, resolved.Root, resolved.Abs, resolved.DisplayPath,
			findDepthUnbounded, maxResults, 0, nameGlob, entryType, sampler.add, &resp,
			&skipped, &matchedTotal, &truncated, &deeperPathsOmitted); err != nil {
			return nil, 0, err
		}
	}
	sampler.sort()
	return sampler.sample, matchedTotal, nil
}

type findResultSampler struct {
	capacity int
	seed     uint64
	total    int
	sample   []findResult
}

func (s *findResultSampler) add(result findResult) {
	s.total++
	if s.capacity <= 0 {
		return
	}
	if len(s.sample) < s.capacity {
		s.sample = append(s.sample, result)
		return
	}
	pick := splitMix64(s.seed+uint64(s.total)) % uint64(s.total)
	if pick < uint64(s.capacity) {
		s.sample[pick] = result
	}
}

func (s *findResultSampler) sort() {
	sort.Slice(s.sample, func(i, j int) bool {
		if s.sample[i].Path != s.sample[j].Path {
			return s.sample[i].Path < s.sample[j].Path
		}
		return s.sample[i].Type < s.sample[j].Type
	})
}

func grepProbeRecords(label string, matches []grepMatch) []evidence.Record {
	var records []evidence.Record
	for i, m := range matches {
		excerpt := strings.TrimSpace(m.Content)
		if excerpt == "" {
			continue
		}
		line := m.Line
		records = append(records, evidence.Record{
			Handle:     fmt.Sprintf("snap#%d", i+1),
			Kind:       "grep",
			Shape:      evidence.ShapeFileRegion,
			SourceTool: "survey_probe",
			Survey:     true,
			Path:       m.Path,
			LineRanges: []evidence.LineRange{{Start: line, End: line}},
			Body:       []string{probeRecordBody(label, line, excerpt)},
		})
	}
	return records
}

func findProbeRecords(label string, results []findResult) []evidence.Record {
	var records []evidence.Record
	for i, r := range results {
		body := fmt.Sprintf("%s %s", r.Type, r.Path)
		records = append(records, evidence.Record{
			Handle:     fmt.Sprintf("snap#%d", i+1),
			Kind:       "find",
			Shape:      evidence.ShapeFileRegion,
			SourceTool: "survey_probe",
			Survey:     true,
			Path:       r.Path,
			LineRanges: []evidence.LineRange{{Start: 1, End: 1}},
			Body:       []string{probeRecordBody(label, 1, body)},
		})
	}
	return records
}

func probeRecordBody(label string, line int, excerpt string) string {
	excerpt = strings.TrimSpace(excerpt)
	if line > 0 {
		return fmt.Sprintf("[%s] %d: %s", label, line, excerpt)
	}
	return fmt.Sprintf("[%s] %s", label, excerpt)
}
