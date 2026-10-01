package decide

import (
	"sort"
	"sync"
)

// RerankLedger collects one tool invocation's rerank outcomes so its result
// can say what the engine ranked.
type RerankLedger struct {
	mu       sync.Mutex
	outcomes []Outcome
}

// NewRerankLedger returns an empty ledger.
func NewRerankLedger() *RerankLedger {
	return &RerankLedger{}
}

// Record keeps one outcome.
func (l *RerankLedger) Record(o Outcome) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.outcomes = append(l.outcomes, o)
}

// RerankReceipt is what a tool result says about the engine's ranking within
// it: the calls that answered, summed, and the sites they ranked.
type RerankReceipt struct {
	Engine string   `json:"engine"`
	Sites  []string `json:"sites"`
	// Candidates is how many the sites offered; Scored how many reached the engine.
	Candidates int   `json:"candidates"`
	Scored     int   `json:"scored"`
	ElapsedMs  int64 `json:"elapsed_ms"`
}

// Receipt sums the calls the engine answered, or is nil when every call
// abstained, so a result without an engine carries no receipt.
func (l *RerankLedger) Receipt() *RerankReceipt {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var receipt *RerankReceipt
	sites := map[string]bool{}
	for _, o := range l.outcomes {
		if o.Abstained {
			continue
		}
		if receipt == nil {
			receipt = &RerankReceipt{Engine: o.Engine.Name}
		}
		receipt.Candidates += o.Candidates
		receipt.Scored += o.Scored
		receipt.ElapsedMs += o.Elapsed.Milliseconds()
		sites[string(o.Site)] = true
	}
	if receipt == nil {
		return nil
	}
	for site := range sites {
		receipt.Sites = append(receipt.Sites, site)
	}
	sort.Strings(receipt.Sites)
	return receipt
}

// WithLedger returns a copy of the reranker that records its outcomes.
func (r Reranker) WithLedger(l *RerankLedger) Reranker {
	r.Ledger = l
	return r
}
