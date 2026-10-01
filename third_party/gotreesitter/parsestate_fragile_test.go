//go:build gts_parsercorephase0

package gotreesitter

import (
	"testing"

	core "github.com/odvcencio/gotreesitter/internal/parsercorephase0"
)

// TestCompactMaterializedFragileBitReachesPublicNode proves Phase-3 Lane 3
// review amendment 7 end to end: a compact subtreeRecord marked fragile (the
// reduce-under-ambiguity path, internal/parsercorephase0/core.go) has its bit
// threaded through MaterializationSubtreeView.Fragile onto the public node so
// node.isFragile() reports it. The compact route declines missing/error nodes,
// so a clean visible fragile record is fragile ONLY if markFragile stamped it
// (isFragile is otherwise false for a clean node), making this a real proof of
// the threading rather than the unconditional missing/error fragility.
func TestCompactMaterializedFragileBitReachesPublicNode(t *testing.T) {
	runner, err := newParserCoreFreshFullRunner(parserCoreWarmGoScanner, parserCoreFreshFullCanonicalOptions())
	if err != nil {
		t.Fatal(err)
	}
	p := runner.parser

	type key struct {
		sym    Symbol
		sb, eb uint32
	}

	for _, row := range diagnosticParserCoreCanonicalAdmissions {
		row := row
		t.Run(row.id, func(t *testing.T) {
			fixture := loadDiagnosticParserCoreCanonicalFixture(t, row.id)
			scheduler, ts, err := runner.executeSchedulerOpen(fixture.Source, runner.compact, true)
			if err != nil {
				t.Fatalf("scheduler: %v", err)
			}
			defer ts.Close()
			compact := runner.compact

			// Collect visible fragile record spans from the derivation.
			fragileKeys := map[key]bool{}
			seen := map[core.SubtreeID]bool{}
			stack := append([]core.SubtreeID{}, scheduler.acceptedPayloads...)
			for len(stack) > 0 {
				id := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if seen[id] {
					continue
				}
				seen[id] = true
				v, _ := compact.MaterializationView(id)
				if v.Fragile && !v.Terminal && p.isVisibleSymbol(Symbol(v.Symbol)) {
					fragileKeys[key{Symbol(v.Symbol), v.StartByte, v.EndByte}] = true
				}
				stack = append(stack, v.Children...)
			}
			if len(fragileKeys) == 0 {
				t.Skipf("%s: derivation has no visible fragile record", row.id)
			}

			// Materialize the SAME derivation and index the public nodes.
			tree, err := runner.materializeSelection(fixture.Source, compact, scheduler)
			if err != nil {
				t.Fatalf("materialize: %v", err)
			}
			defer tree.Release()

			byKey := map[key]*Node{}
			cleanNonFragile := 0
			var walk func(n *Node)
			walk = func(n *Node) {
				if n == nil {
					return
				}
				byKey[key{n.symbol, n.startByte, n.endByte}] = n
				if !n.isFragile() && !n.isMissing() && n.symbol != errorSymbol {
					cleanNonFragile++
				}
				for i := 0; i < n.ChildCount(); i++ {
					walk(n.Child(i))
				}
			}
			walk(tree.root)

			matched, missing, notFragile := 0, 0, 0
			for k := range fragileKeys {
				node, ok := byKey[k]
				if !ok {
					// Collapsed-through or aliased away; not a threading failure.
					missing++
					continue
				}
				if node.isFragile() {
					matched++
				} else {
					notFragile++
					if notFragile <= 5 {
						t.Errorf("%s: fragile compact record %s %d..%d did not stamp node.isFragile()",
							row.id, node.Type(p.language), k.sb, k.eb)
					}
				}
			}
			if matched == 0 {
				t.Fatalf("%s: no fragile compact record reached a public node (visible fragile records=%d, missing=%d)",
					row.id, len(fragileKeys), missing)
			}
			// Sanity: the bit is not universally set -- clean non-fragile nodes exist.
			if cleanNonFragile == 0 {
				t.Fatalf("%s: every node reported fragile; markFragile is over-marking", row.id)
			}
			t.Logf("%s: fragile records matched to public isFragile() nodes=%d (unmatched/collapsed=%d), clean-non-fragile nodes=%d",
				row.id, matched, missing, cleanNonFragile)
		})
	}
}
