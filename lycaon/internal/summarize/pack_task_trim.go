package summarize

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/textrank"
)

// wireFitMaxTrimSteps bounds task-aware trims per call.
const wireFitMaxTrimSteps = 256

// TrimPackOneStep removes one lowest-utility optional row.
// Identity trimming retains one row and records each omission.
func TrimPackOneStep(task string, pack *ContextPack) bool {
	if pack == nil {
		return false
	}
	if len(pack.Imports) > 0 || len(pack.CallSites) > 0 || len(pack.Neighbors) > 0 {
		pack.Imports = nil
		pack.CallSites = nil
		pack.Neighbors = nil
		return true
	}
	if idx, ok := lowestUnbackedSymbolIndex(task, pack.Skeleton, pack.Substance); ok {
		pack.Skeleton = append(pack.Skeleton[:idx:idx], pack.Skeleton[idx+1:]...)
		return true
	}
	if len(pack.Substance) > 1 {
		idx := lowestWindowIndex(task, pack.Substance)
		pack.Substance = append(pack.Substance[:idx:idx], pack.Substance[idx+1:]...)
		return true
	}
	if len(pack.Skeleton) > 0 {
		idx, ok := lowestSkeletonDropIndex(task, pack.Skeleton, false)
		if ok {
			pack.Skeleton = append(pack.Skeleton[:idx:idx], pack.Skeleton[idx+1:]...)
			return true
		}
	}
	if len(pack.Identity) > 1 {
		idx := lowestIdentityDropIndex(task, pack.Identity)
		pack.Identity = append(pack.Identity[:idx:idx], pack.Identity[idx+1:]...)
		noteIdentityTrim(pack)
		return true
	}
	if len(pack.Substance) > 0 {
		pack.Substance = nil
		return true
	}
	if idx, ok := lowestSkeletonDropIndex(task, pack.Skeleton, true); ok {
		pack.Skeleton = append(pack.Skeleton[:idx:idx], pack.Skeleton[idx+1:]...)
		return true
	}
	return false
}

// Unbacked names are navigation; keep implementing source ahead of them.
func lowestUnbackedSymbolIndex(task string, skeleton []PackSymbol, windows []PackWindow) (int, bool) {
	if len(windows) == 0 {
		return -1, false
	}
	var candidates []PackSymbol
	var indexes []int
	for i, symbol := range skeleton {
		if packSymbolProtected(symbol.Kind) || symbolHasWindow(symbol, windows) {
			continue
		}
		candidates = append(candidates, symbol)
		indexes = append(indexes, i)
	}
	idx, ok := lowestSkeletonDropIndex(task, candidates, false)
	if !ok {
		return -1, false
	}
	return indexes[idx], true
}

func symbolHasWindow(symbol PackSymbol, windows []PackWindow) bool {
	for _, window := range windows {
		if window.Path == symbol.Path && symbol.Line >= window.StartLine &&
			window.StartLine > 0 && (window.EndLine <= 0 || symbol.Line <= window.EndLine) {
			return true
		}
	}
	return false
}

const identityTrimGapPrefix = "identity rows omitted for wire budget: "

// noteIdentityTrim records aggregate identity omissions in one gap.
func noteIdentityTrim(pack *ContextPack) {
	for i, g := range pack.Gaps {
		if rest, ok := strings.CutPrefix(g, identityTrimGapPrefix); ok {
			n, _ := strconv.Atoi(rest)
			pack.Gaps[i] = fmt.Sprintf("%s%d", identityTrimGapPrefix, n+1)
			return
		}
	}
	pack.Gaps = append(pack.Gaps, identityTrimGapPrefix+"1")
}

// lowestIdentityDropIndex breaks equal scores toward the ranked tail.
func lowestIdentityDropIndex(task string, identity []PackIdentity) int {
	scores := identityTaskScores(task, identity)
	best := 0
	min := scores[0]
	for i := 1; i < len(scores); i++ {
		if scores[i] < min || (scores[i] == min && i > best) {
			min = scores[i]
			best = i
		}
	}
	return best
}

func identityTaskScores(task string, identity []PackIdentity) []float64 {
	docs := make([][]textrank.Field, len(identity))
	for i, id := range identity {
		docs[i] = []textrank.Field{{Text: id.Path, Weight: 1}}
	}
	return taskFieldScores(task, docs)
}

func lowestWindowIndex(task string, windows []PackWindow) int {
	if len(windows) == 0 {
		return -1
	}
	scores := windowTaskScores(task, windows)
	best := 0
	min := scores[0]
	for i := 1; i < len(scores); i++ {
		if scores[i] < min || (scores[i] == min && i > best) {
			min = scores[i]
			best = i
		}
	}
	return best
}

func lowestSkeletonDropIndex(task string, skeleton []PackSymbol, rollupsOnly bool) (int, bool) {
	if len(skeleton) == 0 {
		return -1, false
	}
	scores := symbolTaskScores(task, skeleton, !rollupsOnly)
	best := -1
	var min float64
	for i, s := range skeleton {
		if rollupsOnly {
			if !packSymbolProtected(s.Kind) {
				continue
			}
		} else if packSymbolProtected(s.Kind) {
			continue
		}
		if best < 0 || scores[i] < min || (scores[i] == min && i > best) {
			min = scores[i]
			best = i
		}
	}
	return best, best >= 0
}

func packSymbolProtected(kind string) bool {
	return kind == KindDirectoryRollup || kind == "directory_map"
}

func windowTaskScores(task string, windows []PackWindow) []float64 {
	docs := make([][]textrank.Field, len(windows))
	for i, w := range windows {
		docs[i] = []textrank.Field{{Text: w.Path, Weight: 3}, {Text: w.Symbol, Weight: 4}}
	}
	return taskFieldScores(task, docs)
}

func symbolTaskScores(task string, skeleton []PackSymbol, protectCoverage bool) []float64 {
	docs := make([][]textrank.Field, len(skeleton))
	for i, s := range skeleton {
		docs[i] = []textrank.Field{{Text: s.Path, Weight: 3}, {Text: s.Name + " " + s.Kind, Weight: 4}}
	}
	scores := taskFieldScores(task, docs)
	if protectCoverage {
		for i, s := range skeleton {
			if packSymbolProtected(s.Kind) {
				scores[i] = math.MaxFloat64
			}
		}
	}
	return scores
}
