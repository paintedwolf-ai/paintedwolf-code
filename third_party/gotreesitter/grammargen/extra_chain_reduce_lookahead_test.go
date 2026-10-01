package grammargen

import "testing"

func TestNonterminalExtraStartClosesSingleReduceState(t *testing.T) {
	g := NewGrammar("visible_extra_boundary")
	g.Define("source_file", Repeat1(Sym("item")))
	g.Define("item", Seq(Str("impl"), Str("{}")))
	g.Define("line_comment", Seq(Str("//"), Token(Pat(`[^\n]*`))))
	g.SetExtras(Pat(`[ \n]+`), Sym("line_comment"))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	tables, _, err := buildLRTablesWithProvenance(ng)
	if err != nil {
		t.Fatalf("build LR tables: %v", err)
	}
	itemSyms := diagFindAllSymbols(ng, "item")
	sourceSyms := diagFindAllSymbols(ng, "source_file")
	slashSyms := diagFindAllSymbols(ng, "//")
	if len(itemSyms) != 1 || len(sourceSyms) != 1 || len(slashSyms) != 1 {
		t.Fatalf("expected source_file, item and // symbols, got source=%v item=%v slash=%v", sourceSyms, itemSyms, slashSyms)
	}

	found := false
	foundStartReduce := false
	for state, bySym := range tables.ActionTable {
		for _, action := range bySym[slashSyms[0]] {
			if action.kind != lrReduce || action.prodIdx < 0 || action.prodIdx >= len(ng.Productions) {
				continue
			}
			switch ng.Productions[action.prodIdx].LHS {
			case itemSyms[0]:
				t.Logf("state %d reduces item before visible extra start: %s", state, diagFormatActions(ng, bySym[slashSyms[0]]))
				found = true
			case sourceSyms[0]:
				foundStartReduce = true
			}
		}
	}
	if !found {
		t.Fatalf("expected item reduce on visible extra start //")
	}
	if foundStartReduce {
		t.Fatalf("did not expect start-symbol reduce on visible extra start //")
	}
}
