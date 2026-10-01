package webresearch

import "testing"

func TestSeedBudgetForHitsScalesWithMaxResults(t *testing.T) {
	b := seedBudgetForHits(5)
	if b.hitTarget != 5 {
		t.Fatalf("hitTarget = %d want 5", b.hitTarget)
	}
	// Lead breadth exceeds the per-host result cap.
	if b.minLeads < 6 || b.minLeads > 10 {
		t.Fatalf("minLeads = %d want slim 6-10", b.minLeads)
	}
	if b.maxSeeds < 2 || b.maxSeeds > maxSeedsPerQuery {
		t.Fatalf("maxSeeds = %d want small extra-root ask", b.maxSeeds)
	}
	b8 := seedBudgetForHits(8)
	if b8.minLeads < b.minLeads || b8.maxLeads <= b8.minLeads {
		t.Fatalf("budget for 8 hits: %+v", b8)
	}
	if b8.maxLeads > maxLeadsPerQuery {
		t.Fatalf("maxLeads = %d exceeds cap", b8.maxLeads)
	}
	b20 := seedBudgetForHits(20)
	if b20.maxLeads <= b8.maxLeads || b20.maxLeads > maxLeadsPerQuery {
		t.Fatalf("budget for 20 hits: %+v", b20)
	}
}
