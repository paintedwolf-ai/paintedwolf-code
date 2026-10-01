package summarize

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/decide"
)

const leftoverNextActionsCap = 6

// assembleContextPack fills identity, breadth, depth, then fit tiers.
func assembleContextPack(ctx context.Context, rr decide.Reranker, task string, structure []StructureCandidate, caps Caps, fit FitEdges) (ContextPack, CuratorStats, []NextAction) {
	budget := caps.Pack.InputBudgetTokens
	if budget <= 0 {
		budget = DefaultCaps().Pack.InputBudgetTokens
	}
	return assembleContextPackBudget(ctx, rr, task, structure, caps, fit, budget)
}

// assembleContextPackBudget is the knapsack with an explicit token budget;
// the allocator drills children under a sub-allocation.
func assembleContextPackBudget(ctx context.Context, rr decide.Reranker, task string, structure []StructureCandidate, caps Caps, fit FitEdges, budget int) (ContextPack, CuratorStats, []NextAction) {
	if budget <= 0 {
		budget = caps.Pack.InputBudgetTokens
		if budget <= 0 {
			budget = DefaultCaps().Pack.InputBudgetTokens
		}
	}
	breadthPct := caps.Pack.BreadthFractionPct
	if breadthPct <= 0 {
		breadthPct = DefaultCaps().Pack.BreadthFractionPct
	}
	breadthBudget := budget * breadthPct / 100
	if breadthBudget < 1 {
		breadthBudget = 1
	}

	pile := buildDefinitionPile(structure)
	pile = rankDefinitions(ctx, rr, task, pile)

	pack := ContextPack{}
	stats := CuratorStats{PrimaryLimitReason: "none"}
	spent := 0

	identityBudget := budget
	softPaths := caps.Pack.MultiPathIdentitySoftPaths
	if softPaths <= 0 {
		softPaths = DefaultCaps().Pack.MultiPathIdentitySoftPaths
	}
	frac := caps.Pack.MultiPathIdentityFractionPct
	if frac <= 0 {
		frac = DefaultCaps().Pack.MultiPathIdentityFractionPct
	}
	if uniquePilePaths(pile) >= softPaths {
		identityBudget = budget * frac / 100
		if identityBudget < 1 {
			identityBudget = 1
		}
	}

	identityBudget = min(identityBudget, max(1, breadthBudget/2))

	// Admit one identity per ranked path until its budget fills.
	seenPath := map[string]bool{}
	for _, d := range pile {
		if d.RelPath == "" || seenPath[d.RelPath] {
			continue
		}
		id := PackIdentity{
			Path: d.RelPath, Kind: d.FileKind, LineCount: d.LineCount, ParseHealth: d.ParseHealth,
			ContentHash: d.ContentHash, Language: d.Language, OutlineSource: d.OutlineSource,
			Parses: d.Parses, Errors: d.Errors, ErrorKind: d.ErrorKind, LogDigest: d.LogDigest, ImportPath: d.ImportPath,
		}
		if id.Kind == "" {
			id.Kind = StructureKindFile
		}
		if id.ParseHealth == "" {
			id.ParseHealth = parseHealthOK
		}
		cost := caps.EstimateTokens(identityLine(id))
		if len(pack.Identity) > 0 && spent+cost > identityBudget {
			if stats.PrimaryLimitReason == "none" {
				stats.PrimaryLimitReason = "pack_budget"
			}
			break
		}
		seenPath[d.RelPath] = true
		pack.Identity = append(pack.Identity, id)
		spent += cost
	}

	// Admit skeleton rows only for represented paths.
	admittedIdx := map[int]bool{}
	for i, d := range pile {
		if !seenPath[d.RelPath] {
			continue
		}
		line := skeletonLine(d)
		cost := caps.EstimateTokens(line)
		if spent+cost > budget {
			if stats.PrimaryLimitReason == "none" {
				stats.PrimaryLimitReason = "pack_budget"
			}
			break
		}
		if len(pack.Skeleton) > 0 && spent+cost > breadthBudget {
			break
		}
		pack.Skeleton = append(pack.Skeleton, PackSymbol{
			Path: d.RelPath, Kind: d.Kind, Name: d.Name, Line: d.Line,
		})
		admittedIdx[i] = true
		spent += cost
		stats.BreadthAdmits++
	}

	// Depth — symbol windows only for defs that already have skeleton admits.
	windowLines := caps.Gather.SymbolWindowLines
	headLinesByPath := map[string][]string{}
	var windows []PackWindow
	for i, d := range pile {
		if !admittedIdx[i] || d.Pinned || d.Kind == "directory_map" || d.Kind == KindDirectoryRollup {
			continue
		}
		lines := headLinesByPath[d.RelPath]
		if lines == nil && d.Head != "" {
			lines = strings.Split(d.Head, "\n")
			headLinesByPath[d.RelPath] = lines
		}
		w, ok := symbolWindowLines(d, windowLines, lines)
		if !ok {
			continue
		}
		merged := mergeOverlappingWindows(append(append([]PackWindow(nil), windows...), w))
		cost := windowTokens(caps, merged) - windowTokens(caps, windows)
		if spent+cost > budget {
			if stats.PrimaryLimitReason == "none" {
				stats.PrimaryLimitReason = "pack_budget"
			}
			continue
		}
		windows = merged
		spent += cost
		stats.DepthAdmits++
		stats.DeepenHits++
	}
	pack.Substance = mergeOverlappingWindows(windows)

	// Fit tier — neighbors then call_sites; only post-depth slack; drop first.
	spent = admitFitEdges(ctx, rr, task, fit, caps, budget, &pack, &stats, spent)

	leftoverDefs := appendGapsFromPile(pile, admittedIdx, &pack)
	stats.BudgetTokensSpent = spent
	if len(pack.Gaps) > 0 && stats.PrimaryLimitReason == "none" {
		stats.PrimaryLimitReason = "pack_budget"
	}

	next := leftoverNextActions(leftoverDefs, windowLines)
	return pack, stats, next
}

func appendGapsFromPile(pile []DefinitionItem, admittedIdx map[int]bool, pack *ContextPack) []DefinitionItem {
	var leftoverDefs []DefinitionItem
	seenGap := map[string]bool{}
	for i, d := range pile {
		if d.Pinned || admittedIdx[i] {
			continue
		}
		key := d.RelPath
		if d.Line > 0 {
			key = fmt.Sprintf("%s:%d", d.RelPath, d.Line)
		}
		if key == "" || seenGap[key] {
			continue
		}
		seenGap[key] = true
		pack.Gaps = append(pack.Gaps, key)
		leftoverDefs = append(leftoverDefs, d)
	}
	return leftoverDefs
}

func uniquePilePaths(pile []DefinitionItem) int {
	seen := map[string]bool{}
	for _, d := range pile {
		if d.RelPath == "" || seen[d.RelPath] {
			continue
		}
		seen[d.RelPath] = true
	}
	return len(seen)
}

// admitFitEdges spends remaining budget on ranked imports, neighbors, then
// call-sites. Fit is optional: when the budget is already full, nothing is admitted.
func admitFitEdges(ctx context.Context, rr decide.Reranker, task string, fit FitEdges, caps Caps, budget int, pack *ContextPack, stats *CuratorStats, spent int) int {
	fitPending := len(fit.Imports) > 0 || len(fit.Neighbors) > 0 || len(fit.CallSites) > 0
	for _, e := range rankImportEdges(ctx, rr, task, fit.Imports) {
		cost := caps.EstimateTokens(importEdgeLine(e))
		if spent+cost > budget {
			if stats.PrimaryLimitReason == "none" && fitPending {
				stats.PrimaryLimitReason = "pack_budget"
			}
			return spent
		}
		pack.Imports = append(pack.Imports, e)
		spent += cost
		stats.FitAdmits++
	}
	for _, n := range rankNeighborStubs(ctx, rr, task, fit.Neighbors) {
		cost := caps.EstimateTokens(neighborLine(n))
		if spent+cost > budget {
			if stats.PrimaryLimitReason == "none" && fitPending {
				stats.PrimaryLimitReason = "pack_budget"
			}
			return spent
		}
		pack.Neighbors = append(pack.Neighbors, n)
		spent += cost
		stats.FitAdmits++
	}
	for _, cs := range rankCallSites(ctx, rr, task, fit.CallSites) {
		cost := caps.EstimateTokens(callSiteLine(cs))
		if spent+cost > budget {
			if stats.PrimaryLimitReason == "none" && fitPending {
				stats.PrimaryLimitReason = "pack_budget"
			}
			return spent
		}
		pack.CallSites = append(pack.CallSites, cs)
		spent += cost
		stats.FitAdmits++
	}
	return spent
}

func leftoverNextActions(leftover []DefinitionItem, windowLines int) []NextAction {
	if windowLines <= 0 {
		windowLines = 24
	}
	capN := leftoverNextActionsCap
	var out []NextAction
	seen := map[string]bool{}
	for _, d := range leftover {
		if len(out) >= capN {
			break
		}
		if d.RelPath == "" || seen[d.RelPath] {
			continue
		}
		seen[d.RelPath] = true
		lines := ""
		why := "Inspect source detail"
		if d.Line > 0 {
			half := windowLines / 2
			start := d.Line - half
			if start < 1 {
				start = 1
			}
			end := start + windowLines - 1
			lines = fmt.Sprintf("%d-%d", start, end)
			why = fmt.Sprintf("Inspect %s around %s", d.RelPath, d.Name)
			if d.Name == "" {
				why = fmt.Sprintf("Inspect %s:%d", d.RelPath, d.Line)
			}
		}
		out = append(out, NextAction{
			Tool: "read", Path: d.RelPath, Lines: lines, Why: why,
		})
	}
	return out
}

func windowTokens(caps Caps, windows []PackWindow) int {
	total := 0
	for _, w := range windows {
		total += caps.EstimateTokens(windowLine(w))
	}
	return total
}
