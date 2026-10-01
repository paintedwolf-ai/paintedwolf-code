package survey

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/summarize"
)

var errSummaryReadBudget = errors.New("summary source read budget exhausted")

func trimSummaryUTF8Tail(raw []byte) []byte {
	start := len(raw) - 1
	for start > 0 && !utf8.RuneStart(raw[start]) {
		start--
	}
	if start >= 0 && !utf8.FullRune(raw[start:]) {
		return raw[:start]
	}
	return raw
}

func (g *summarizeGatherer) reserveSourceRead(abs string, size int64) (int64, error) {
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	if g.readReservations == nil {
		g.readReservations = map[string]int64{}
	}
	if _, ok := g.readReservations[abs]; ok {
		g.sourceLimited = true
		return 0, errSummaryReadBudget
	}
	if g.caps.Gather.MaxFilesRead > 0 && g.sourceFiles >= g.caps.Gather.MaxFilesRead {
		g.sourceLimited = true
		return 0, errSummaryReadBudget
	}
	remaining := int64(g.caps.Gather.MaxBytes) - g.sourceBytes
	if remaining <= 0 {
		g.sourceLimited = true
		return 0, errSummaryReadBudget
	}
	n := min(size, remaining, int64(g.caps.Gather.FileReadBytes))
	g.readReservations[abs] = n
	g.sourceFiles++
	g.sourceBytes += n
	if n < size {
		g.sourceLimited = true
		if g.truncatedPaths == nil {
			g.truncatedPaths = map[string]bool{}
		}
		g.truncatedPaths[abs] = true
	}
	return n, nil
}

func (g *summarizeGatherer) markPartialStructure(abs string, sc *summarize.StructureCandidate) {
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	if !g.truncatedPaths[abs] {
		return
	}
	sc.LineCount = 0
	sc.Parses = nil
	sc.Errors = nil
	sc.ErrorKind = ""
}

type summaryCountingReader struct {
	reader io.Reader
	read   int64
}

func (r *summaryCountingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read += int64(n)
	return n, err
}

func (g *summarizeGatherer) stampWork(res *summarize.Result) {
	res.Orchestration.Curator.DirectoryEntriesRead = g.directoryEntries
	res.Orchestration.Curator.DirectoriesOpened = g.directoriesOpened
	for _, r := range g.treeReaders {
		res.Orchestration.Curator.MetadataRowsRead += r.RowsRead
	}
	res.Orchestration.Curator.SourceFilesRead = g.sourceFiles
	res.Orchestration.Curator.SourceBytesRead = g.sourceReadBytes
	if len(g.skippedPaths) > 0 {
		res.Coverage.Complete = false
		res.Pack.Gaps = append(res.Pack.Gaps, fmt.Sprintf("Source detail omitted %d unreadable, non-text or budget-limited paths; this pack does not establish their contents.", len(g.skippedPaths)))
	}
	if g.sourceLimited {
		res.Pack.Gaps = append(res.Pack.Gaps, "Source detail reached the read budget; summarize a narrower path for more detail.")
	}
}

func (g *summarizeGatherer) recordSourceBytes(n int64) {
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	g.sourceReadBytes += n
}

func (g *summarizeGatherer) sourceBudgetExhausted() bool {
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	full := g.sourceBytes >= int64(g.caps.Gather.MaxBytes) || (g.caps.Gather.MaxFilesRead > 0 && g.sourceFiles >= g.caps.Gather.MaxFilesRead)
	g.sourceLimited = g.sourceLimited || full
	return full
}

func (g *summarizeGatherer) noteSkippedPath(path string) {
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	if g.skippedPaths == nil {
		g.skippedPaths = map[string]bool{}
	}
	g.skippedPaths[path] = true
}

func (g *summarizeGatherer) noMaterialData(req summarize.Request) map[string]any {
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	roots := append([]string(nil), req.Paths...)
	if len(roots) == 0 && req.Path != "" {
		roots = []string{req.Path}
	}
	skipped := make([]string, 0, len(g.skippedPaths))
	for path := range g.skippedPaths {
		skipped = append(skipped, path)
	}
	sort.Strings(skipped)
	path := ""
	if len(roots) == 1 {
		path = roots[0]
	}
	return map[string]any{
		"path": path, "paths": roots, "pattern": req.Pattern,
		"summary_source_limited": g.sourceLimited,
		"summary_skipped_count":  len(skipped),
		"summary_skipped_paths":  skipped[:min(len(skipped), 10)],
	}
}

func (g *summarizeGatherer) noteNonTextPath(path string) {
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	if g.nonTextPaths == nil {
		g.nonTextPaths = map[string]bool{}
	}
	g.nonTextPaths[path] = true
}

func (g *summarizeGatherer) actionableSources(actions []summarize.NextAction) []summarize.NextAction {
	out := make([]summarize.NextAction, 0, len(actions))
	for _, action := range actions {
		if g.nonTextPaths[action.Path] {
			continue
		}
		out = append(out, action)
	}
	return out
}
