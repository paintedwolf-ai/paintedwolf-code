package decide

import (
	"testing"
	"time"
)

func TestRerankLedgerSumsAnsweredCallsOnly(t *testing.T) {
	ledger := NewRerankLedger()
	if ledger.Receipt() != nil {
		t.Fatal("an empty ledger must carry no receipt")
	}
	engine := Engine{Name: "Bialy", Model: "mmbert-base", Head: "code-rank"}
	ledger.Record(Outcome{Site: SiteSummarizeDefinitions, Engine: engine, Candidates: 40, Scored: 24, Elapsed: 90 * time.Millisecond})
	ledger.Record(Outcome{Site: SiteSummarizeStructure, Candidates: 12, Abstained: true, Reason: "nothing to rank", Elapsed: time.Millisecond})
	ledger.Record(Outcome{Site: SiteSummarizeStructure, Engine: engine, Candidates: 30, Scored: 24, Elapsed: 60 * time.Millisecond})
	got := ledger.Receipt()
	if got == nil {
		t.Fatal("answered calls must produce a receipt")
	}
	if got.Engine != "Bialy" || got.Candidates != 70 || got.Scored != 48 || got.ElapsedMs != 150 {
		t.Fatalf("receipt = %+v", got)
	}
	if len(got.Sites) != 2 || got.Sites[0] != "summarize_definitions" || got.Sites[1] != "summarize_structure" {
		t.Fatalf("sites = %v, want each answered site once, sorted", got.Sites)
	}

	abstaining := NewRerankLedger()
	abstaining.Record(Outcome{Site: SiteWebPages, Abstained: true, Reason: "engine unavailable"})
	if abstaining.Receipt() != nil {
		t.Fatal("a ledger of abstentions must carry no receipt")
	}
	if (*RerankLedger)(nil).Receipt() != nil {
		t.Fatal("a nil ledger must carry no receipt")
	}
}

func TestRerankerRecordsOutcomesOnItsLedger(t *testing.T) {
	ledger := NewRerankLedger()
	rr := Reranker{}.WithLedger(ledger)
	if _, out := rr.Rerank(t.Context(), SiteWebPages, "task", []float64{1, 2}, []string{"a", "b"}); !out.Abstained {
		t.Fatal("a reranker without an engine abstains")
	}
	if len(ledger.outcomes) != 1 || !ledger.outcomes[0].Abstained || ledger.outcomes[0].Site != SiteWebPages {
		t.Fatalf("ledger outcomes = %+v, want the abstention recorded", ledger.outcomes)
	}
}
