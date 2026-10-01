package decide

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/observability"
)

var log = observability.LazyComponent("decide")

// rankScoreCeiling is the top of the engine's relevance rubric. Scores are
// normalized to a unit interval so every site blends on its own scale.
const rankScoreCeiling = 4.0

// Reranker rescores a site's lexical candidates through the engine. The zero
// value abstains everywhere, so a package that carries one never branches on
// its presence.
type Reranker struct {
	Decider  Decider
	Policies Policies
	// Observe receives every call, abstentions included; nil observes nothing.
	Observe func(Observation)
	// Ledger collects the outcomes of one invocation for its result; nil keeps none.
	Ledger *RerankLedger
}

// Outcome is what one rerank call established.
type Outcome struct {
	Site   Site
	Engine Engine
	// Candidates is the size of the list; Scored is how many reached the engine.
	Candidates int
	Scored     int
	Elapsed    time.Duration
	// Abstained is true when the lexical order stands; Reason says why.
	Abstained bool
	Reason    string
}

// Observation is the full record of one call: what the site offered, what the
// engine answered, and what the site received back.
type Observation struct {
	Site    Site
	Task    string
	Texts   []string
	Lexical []float64
	// Unit holds the engine's unit relevance for every scored candidate index.
	Unit    map[int]float64
	Blended []float64
	Outcome Outcome
}

// Active reports whether a site should build candidate text: its policy is
// enabled and either an engine with the site's head can answer or an
// observer wants to see what the site would have offered.
func (r Reranker) Active(site Site) bool {
	if !r.Policies.For(site).Enabled {
		return false
	}
	return r.Observe != nil || r.serves(site)
}

// serves reports whether an engine can rank the site with its trained head.
func (r Reranker) serves(site Site) bool {
	return r.Decider != nil && r.Decider.Available() && r.Decider.Loaded(headForSite(site))
}

// Rerank returns lexical with the engine's relevance blended into the top
// candidates. lexical and texts are parallel; the result is always a fresh
// slice of the same length. Any fault, deadline, or disabled policy returns
// the lexical scores unchanged and says so in the outcome.
func (r Reranker) Rerank(ctx context.Context, site Site, task string, lexical []float64, texts []string) ([]float64, Outcome) {
	started := time.Now()
	out := append([]float64(nil), lexical...)
	policy := r.Policies.For(site)
	outcome := Outcome{Site: site, Candidates: len(lexical), Abstained: true}
	obs := Observation{Site: site, Task: task, Texts: texts, Lexical: lexical}
	finish := func() ([]float64, Outcome) {
		outcome.Elapsed = time.Since(started)
		obs.Blended = out
		obs.Outcome = outcome
		r.record(obs)
		r.Ledger.Record(outcome)
		return out, outcome
	}
	switch {
	case len(lexical) != len(texts):
		outcome.Reason = "candidate mismatch"
		return finish()
	case !policy.Enabled:
		outcome.Reason = "site disabled"
		return finish()
	case r.Decider == nil || !r.Decider.Available():
		outcome.Reason = "engine unavailable"
		return finish()
	case !r.Decider.Loaded(headForSite(site)):
		outcome.Reason = "head not loaded"
		return finish()
	case strings.TrimSpace(task) == "":
		outcome.Reason = "no task"
		return finish()
	case len(texts) < 2:
		outcome.Reason = "nothing to rank"
		return finish()
	}
	top := topIndices(lexical, policy.MaxCandidates)
	scored := make([]string, len(top))
	for i, idx := range top {
		scored[i] = texts[idx]
	}
	scores, engine, err := r.rankWithin(ctx, headForSite(site), policy, task, scored)
	outcome.Engine = engine
	if err != nil {
		outcome.Reason = abstainReason(err)
		return finish()
	}
	obs.Unit = make(map[int]float64, len(top))
	for i, idx := range top {
		unit := unitRelevance(scores[i])
		obs.Unit[idx] = unit
		out[idx] += policy.Weight * unit
	}
	outcome.Scored = len(top)
	outcome.Abstained = false
	return finish()
}

// headForSite names the head a site ranks with: web pages have their own,
// every code seam shares the code head.
func headForSite(site Site) Head {
	if site == SiteWebPages {
		return HeadWebRank
	}
	return HeadCodeRank
}

// rankWithin scores candidates chunk by chunk under the policy deadline. The
// engine call runs aside so a warming engine cannot hold the site past the
// deadline; a late answer is dropped.
func (r Reranker) rankWithin(ctx context.Context, head Head, policy RerankPolicy, task string, texts []string) ([]float64, Engine, error) {
	callCtx, cancel := context.WithTimeout(ctx, policy.Deadline)
	defer cancel()
	type answer struct {
		scores []float64
		engine Engine
		err    error
	}
	done := make(chan answer, 1)
	go func() {
		scores := make([]float64, 0, len(texts))
		var engine Engine
		for start := 0; start < len(texts); start += policy.Chunk {
			end := min(start+policy.Chunk, len(texts))
			part, eng, err := r.Decider.Rank(callCtx, head, task, texts[start:end])
			if err != nil {
				done <- answer{err: err}
				return
			}
			engine = eng
			scores = append(scores, part...)
		}
		done <- answer{scores: scores, engine: engine}
	}()
	select {
	case a := <-done:
		return a.scores, a.engine, a.err
	case <-callCtx.Done():
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return nil, Engine{}, ErrDeadline
		}
		return nil, Engine{}, callCtx.Err()
	}
}

func (r Reranker) record(obs Observation) {
	o := obs.Outcome
	if o.Abstained && o.Reason == "site disabled" {
		log.Debug("rerank skipped", "site", o.Site, "candidates", o.Candidates)
	} else {
		log.Info("rerank",
			"site", o.Site,
			"engine", o.Engine.Name,
			"model", o.Engine.Model,
			"head", o.Engine.Head,
			"candidates", o.Candidates,
			"scored", o.Scored,
			"elapsed_ms", o.Elapsed.Milliseconds(),
			"abstained", o.Abstained,
			"reason", o.Reason)
	}
	if r.Observe != nil {
		r.Observe(obs)
	}
}

// topIndices returns the indices of the k highest lexical scores in stable
// descending order.
func topIndices(lexical []float64, k int) []int {
	order := make([]int, len(lexical))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return lexical[order[a]] > lexical[order[b]] })
	if k > 0 && k < len(order) {
		order = order[:k]
	}
	return order
}

// unitRelevance maps an engine rubric score onto [0, 1].
func unitRelevance(score float64) float64 {
	if math.IsNaN(score) {
		return 0
	}
	return math.Min(math.Max(score/rankScoreCeiling, 0), 1)
}

func abstainReason(err error) string {
	switch {
	case errors.Is(err, ErrDisabled):
		return "engine disabled"
	case errors.Is(err, ErrUnavailable):
		return "engine unavailable"
	case errors.Is(err, ErrDeadline), errors.Is(err, context.DeadlineExceeded):
		return "deadline exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "engine fault: " + err.Error()
	}
}
