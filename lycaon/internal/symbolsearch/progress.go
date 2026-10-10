package symbolsearch

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/project"
)

const retainedDeclarationBytes = 1 << 20

// Progress is disposable, caller-serialized state. Its caller binds
// it to query, scope, catalog revisions, and content epochs before every use.
// Only one candidate batch and the best bounded result set survive a request.
type Progress struct {
	phase         int
	pending       []project.DeclarationSearchHit
	discoveryDone bool
	matches       []Match
	gaps          []project.DeclarationGap
	discoveryGaps []project.DeclarationGap
}

func (r *symbolSearchRun) advance(ctx context.Context, query string, limit int, wall, abbreviationWall, outlineWall time.Duration, state *Progress, resume bool) (bool, error) {
	discoveryEnd := time.Now().Add(wall)
	outlineLeft := outlineWall
	for state.phase < 2 {
		match, pattern := project.DeclarationMatchSubstring, query
		if r.exact {
			match = project.DeclarationMatchWholeWord
		}
		if state.phase == 1 {
			hump, ok := symbolHumpPattern(query)
			if r.exact || !ok || r.countBetterThan(symbolTierPrefix) > 0 {
				state.phase = 2
				break
			}
			if r.countBetterThan(symbolTierHump) >= limit {
				r.saveProgress(state)
				for _, gap := range state.gaps {
					r.coverage.Gaps = mergeDeclarationGap(r.coverage.Gaps, gap)
				}
				r.coverage.Gaps = append(r.coverage.Gaps, state.discoveryGaps...)
				r.incomplete = r.coverage.Incomplete()
				return true, nil
			}
			match, pattern = project.DeclarationMatchRegexp, hump
			discoveryEnd = time.Now().Add(abbreviationWall)
		}
		if len(state.pending) == 0 && !state.discoveryDone {
			remaining := time.Until(discoveryEnd)
			if remaining <= 0 || ctx.Err() != nil || r.filesLeft <= 0 {
				break
			}
			started := time.Now()
			hits, coverage, err := r.search(ctx, project.DeclarationSearchQuery{
				ProjectID: r.p.ID, Roots: r.discoveryRoots(), Pattern: pattern, Match: match,
				ExcludeDirs: append([]string(nil), r.excludes...), HitCap: DiscoveryFileCap,
				Wall: remaining, Continue: true,
			})
			if err != nil {
				if ctx.Err() != nil {
					break
				}
				return false, err
			}
			r.passes = append(r.passes, Pass{Match: match, Hits: len(hits), Partial: coverage.Incomplete(), Discovery: time.Since(started)})
			state.pending = hits
			state.discoveryDone = !coverage.Limited
			state.discoveryGaps = nil
			for _, gap := range coverage.Gaps {
				if declarationRetryable(gap.Reason) {
					state.discoveryDone = false
					state.discoveryGaps = append(state.discoveryGaps, gap)
				} else {
					state.gaps = mergeDeclarationGap(state.gaps, gap)
				}
			}
			if coverage.Limited && !resume {
				state.gaps = mergeDeclarationGap(state.gaps, project.DeclarationGap{Reason: "symbol_budget", Limit: DiscoveryFileCap})
				state.discoveryDone = true
			}
		}
		started := time.Now()
		outlineCtx, cancel := context.WithTimeout(ctx, max(outlineLeft, time.Nanosecond))
		r.outlinePending(outlineCtx, state)
		cancel()
		outlineLeft -= time.Since(started)
		if n := len(r.passes); n > 0 {
			r.passes[n-1].Outline += time.Since(started)
		}
		if len(state.pending) > 0 || !state.discoveryDone {
			break
		}
		state.phase++
		state.discoveryDone = false
	}
	r.saveProgress(state)
	for _, gap := range state.gaps {
		r.coverage.Gaps = mergeDeclarationGap(r.coverage.Gaps, gap)
	}
	r.coverage.Gaps = append(r.coverage.Gaps, state.discoveryGaps...)
	if ctx.Err() != nil {
		r.coverage.Gaps = mergeDeclarationGap(r.coverage.Gaps, project.DeclarationGap{Reason: project.DeclarationTimeBudget})
	}
	if state.phase < 2 {
		reason := project.DeclarationPending
		if !resume {
			reason = project.DeclarationSymbolBudget
		}
		r.coverage.Gaps = mergeDeclarationGap(r.coverage.Gaps, project.DeclarationGap{Reason: reason})
	}
	r.incomplete = r.coverage.Incomplete()
	return false, nil
}

func (r *symbolSearchRun) discoveryRoots() []project.DeclarationSearchRoot {
	roots := make([]project.DeclarationSearchRoot, 0, len(r.roots))
	for _, root := range r.roots {
		roots = append(roots, root.DeclarationSearchRoot)
	}
	return roots
}

func (r *symbolSearchRun) outlinePending(ctx context.Context, state *Progress) {
	files := r.rankCandidates(project.DeclarationFilesFromHits(state.pending))
	consumed := map[project.DeclarationFileKey]bool{}
	for _, file := range files {
		if r.filesLeft <= 0 || ctx.Err() != nil {
			break
		}
		symbols, content, ok := project.ReadSourceDeclarations(ctx, r.p, file.RootID, file.Path)
		if ctx.Err() != nil {
			break
		}
		consumed[project.DeclarationFileKey{file.RootID, file.Path}] = true
		r.filesLeft--
		if n := len(r.passes); n > 0 {
			r.passes[n-1].Files++
		}
		if !ok {
			count := 1
			for _, gap := range state.gaps {
				if gap.Reason == project.DeclarationFilesSkipped {
					count += gap.Count
				}
			}
			state.gaps = mergeDeclarationGap(state.gaps, project.DeclarationGap{Reason: project.DeclarationFilesSkipped, Count: count})
			continue
		}
		r.matches = append(r.matches, r.pick(file, symbols, content)...)
		// Keep only the strongest results plus one probe, independent of tree size.
		r.trimMatches()
	}
	pending := state.pending[:0]
	for _, hit := range state.pending {
		if !consumed[project.DeclarationFileKey{hit.RootID, strings.ReplaceAll(strings.TrimSpace(hit.Path), "\\", "/")}] {
			pending = append(pending, hit)
		}
	}
	clear(state.pending[len(pending):])
	state.pending = pending
}

func (r *symbolSearchRun) trimMatches() {
	sort.SliceStable(r.matches, func(i, j int) bool { return symbolMatchLess(r.matches[i], r.matches[j]) })
	unique := make([]Match, 0, min(len(r.matches), MaxLimit+1))
	bytesLeft := retainedDeclarationBytes
	for _, match := range r.matches {
		if len(unique) > 0 {
			prev := unique[len(unique)-1]
			if prev.RootID == match.RootID && prev.Path == match.Path && prev.Line == match.Line && prev.Name == match.Name && prev.Kind == match.Kind {
				continue
			}
		}
		size := len(match.Name) + len(match.Path) + len(match.Signature) + len(match.Highlights)*16 + 128
		if size > bytesLeft {
			r.coverage.Gaps = mergeDeclarationGap(r.coverage.Gaps, project.DeclarationGap{Reason: project.DeclarationSymbolBudget, Limit: retainedDeclarationBytes})
			continue
		}
		bytesLeft -= size
		match.Name = strings.Clone(match.Name)
		match.Signature = strings.Clone(match.Signature)
		unique = append(unique, match)
		if len(unique) == MaxLimit+1 {
			break
		}
	}
	r.matches = unique
}

func (r *symbolSearchRun) saveProgress(state *Progress) {
	r.trimMatches()
	state.matches = append([]Match(nil), r.matches...)
	for _, gap := range r.coverage.Gaps {
		if !declarationRetryable(gap.Reason) {
			state.gaps = mergeDeclarationGap(state.gaps, gap)
		}
	}
}

func declarationRetryable(reason project.DeclarationGapReason) bool {
	switch reason {
	case "time_budget", "catalog_warming", "catalog_incomplete", "catalog_refreshing", "index_warming":
		return true
	default:
		return false
	}
}

func mergeDeclarationGap(gaps []project.DeclarationGap, gap project.DeclarationGap) []project.DeclarationGap {
	for i := range gaps {
		if gaps[i].Reason == gap.Reason {
			gaps[i].Count = max(gaps[i].Count, gap.Count)
			return gaps
		}
	}
	return append(gaps, gap)
}

// Rank within a bounded batch before spending the outline allocation.
func (r *symbolSearchRun) rankCandidates(files []project.DeclarationFile) []project.DeclarationFile {
	type candidate struct {
		file  project.DeclarationFile
		tier  symbolMatchTier
		depth int
	}
	ranked := make([]candidate, 0, len(files))
	for _, file := range files {
		tier := symbolTierNone
		for _, snippet := range file.Snippets {
			tier = min(tier, r.matcher.bestTokenTier(snippet))
		}
		ranked = append(ranked, candidate{file, tier, strings.Count(file.Path, "/")})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].tier != ranked[j].tier {
			return ranked[i].tier < ranked[j].tier
		}
		return ranked[i].depth < ranked[j].depth
	})
	for i, candidate := range ranked {
		files[i] = candidate.file
	}
	return files
}
