package survey

import (
	"errors"

	"github.com/lycaon/lycaon/internal/summarize"
)

var errSummaryReadBudget = errors.New("summary source read budget exhausted")

func (g *summarySources) reserveSourceRead(abs string, size int64) (int64, error) {
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

func (g *summarySources) markPartialStructure(abs string, sc *summarize.StructureCandidate) {
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

func (g *summarySources) recordSourceBytes(n int64) {
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	g.sourceReadBytes += n
}

func (g *summarySources) sourceBudgetExhausted() bool {
	g.memoMu.Lock()
	defer g.memoMu.Unlock()
	full := g.sourceBytes >= int64(g.caps.Gather.MaxBytes) || (g.caps.Gather.MaxFilesRead > 0 && g.sourceFiles >= g.caps.Gather.MaxFilesRead)
	g.sourceLimited = g.sourceLimited || full
	return full
}
