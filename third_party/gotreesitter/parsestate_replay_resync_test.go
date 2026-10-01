package gotreesitter_test

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

// TestParseStateReplayResync isolates the two error sources: whether
// advance(recordedPreGoto, node) reproduces parseState exactly (goto/shift
// model), and whether the sibling preGoto chain holds. If the first is ~100%
// and the second is <100%, the sole obstacle to visible-tree replay is
// hidden-node goto transitions (Lane 3 coupling), not the transition model.
func TestParseStateReplayResync(t *testing.T) {
	var totNodes, totPS, totFC, totFCok, totSC, totSCok int
	classAgg := map[string]*gotreesitter.ReplayClassStat{}
	for _, c := range replayCorpus(t) {
		p := gotreesitter.NewParser(c.lang)
		tree, err := p.Parse(c.source)
		if err != nil {
			t.Fatalf("parse %s: %v", c.name, err)
		}
		rep := gotreesitter.ReplayDiffResync(p, tree.RootNode())
		tree.Release()
		totNodes += rep.TotalNodes
		totPS += rep.ParseStateExact
		totFC += rep.FirstChildTotal
		totFCok += rep.FirstChildPreExact
		totSC += rep.SiblingChainTotal
		totSCok += rep.SiblingChainExact
		for _, st := range rep.SortedClassStats() {
			agg := classAgg[st.Class]
			if agg == nil {
				agg = &gotreesitter.ReplayClassStat{Class: st.Class}
				classAgg[st.Class] = agg
			}
			agg.Total += st.Total
			agg.Mismatched += st.Mismatched
		}
	}
	psPct := 100.0 * float64(totPS) / float64(max1(totNodes))
	fcPct := 100.0 * float64(totFCok) / float64(max1(totFC))
	scPct := 100.0 * float64(totSCok) / float64(max1(totSC))
	t.Logf("RESYNC: nodes=%d parseState-exact-given-recorded-preGoto=%d (%.4f%%)", totNodes, totPS, psPct)
	t.Logf("  first-child preGoto == parent preGoto: %d/%d (%.4f%%)", totFCok, totFC, fcPct)
	t.Logf("  sibling chain preGoto == prev advance:  %d/%d (%.4f%%)", totSCok, totSC, scPct)
	t.Logf("  (a low sibling-chain %% with ~100%% parseState-exact proves hidden-node goto is the only gap)")
	for _, k := range sortedKeysByMismatch(classAgg) {
		st := classAgg[k]
		if st.Mismatched == 0 {
			continue
		}
		t.Logf("  parseState-inexact class %-26s total=%-7d inexact=%-7d (%.4f%%)", k, st.Total, st.Mismatched,
			100.0*float64(st.Mismatched)/float64(st.Total))
	}
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

func sortedKeysByMismatch(m map[string]*gotreesitter.ReplayClassStat) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if m[keys[j]].Mismatched > m[keys[i]].Mismatched {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}
