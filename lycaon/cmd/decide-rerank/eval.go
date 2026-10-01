package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/bialy"
)

type evalConfig struct {
	site     decide.Site
	repo     string
	name     string
	units    string
	pairs    string
	jsonOut  string
	dumpOut  string
	limit    int
	noEngine bool
	weight   float64
	k        int
	chunk    int
	deadline time.Duration
}

// pairOutcome is one pair's ranks before and after the engine.
type pairOutcome struct {
	File        string `json:"file"`
	Symbol      string `json:"symbol"`
	Task        string `json:"task"`
	Candidates  int    `json:"candidates"`
	Scored      int    `json:"scored"`
	LexicalRank int    `json:"lexical_rank"`
	BlendedRank int    `json:"blended_rank"`
	Abstained   bool   `json:"abstained"`
	Reason      string `json:"reason,omitempty"`
	ElapsedMS   int64  `json:"elapsed_ms"`
}

type rankMetrics struct {
	MRR    float64 `json:"mrr"`
	NDCG10 float64 `json:"ndcg_10"`
	Hit1   float64 `json:"hit_1"`
	Hit5   float64 `json:"hit_5"`
	Hit10  float64 `json:"hit_10"`
}

type latency struct {
	P50MS int64 `json:"p50_ms"`
	P95MS int64 `json:"p95_ms"`
	MaxMS int64 `json:"max_ms"`
}

type report struct {
	Site       decide.Site         `json:"site"`
	Repo       string              `json:"repo"`
	Engine     decide.Engine       `json:"engine"`
	Policy     decide.RerankPolicy `json:"policy"`
	Pairs      int                 `json:"pairs"`
	Skipped    map[string]int      `json:"skipped,omitempty"`
	Missing    int                 `json:"target_missing"`
	Lexical    rankMetrics         `json:"lexical"`
	Blended    rankMetrics         `json:"blended"`
	Improved   int                 `json:"improved"`
	Regressed  int                 `json:"regressed"`
	Unchanged  int                 `json:"unchanged"`
	Abstained  map[string]int      `json:"abstained,omitempty"`
	Latency    latency             `json:"latency"`
	Outcomes   []pairOutcome       `json:"outcomes"`
	StartedAt  time.Time           `json:"started_at"`
	FinishedAt time.Time           `json:"finished_at"`
}

// dumpRow is one pair's candidate set for offline training.
type dumpRow struct {
	Site       decide.Site     `json:"site"`
	Repo       string          `json:"repo"`
	Task       string          `json:"task"`
	Query      string          `json:"query,omitempty"`
	File       string          `json:"file"`
	Symbol     string          `json:"symbol"`
	Line       int             `json:"line"`
	Lang       string          `json:"lang,omitempty"`
	Candidates []dumpCandidate `json:"candidates"`
}

type dumpCandidate struct {
	Text    string   `json:"text"`
	File    string   `json:"file"`
	Symbol  string   `json:"symbol,omitempty"`
	Line    int      `json:"line,omitempty"`
	Lexical float64  `json:"lexical"`
	Unit    *float64 `json:"unit,omitempty"`
	Target  bool     `json:"target"`
}

func runEval(ctx context.Context, args []string) error {
	fs := newFlags("eval")
	var cfg evalConfig
	site := fs.String("site", "", "reranking site to measure")
	fs.StringVar(&cfg.repo, "repo", "", "repository root the units were harvested from")
	fs.StringVar(&cfg.name, "name", "", "repository name on the units and pairs")
	fs.StringVar(&cfg.units, "units", "", "units JSONL from harvest")
	fs.StringVar(&cfg.pairs, "pairs", "", "pairs JSONL: repo, file, line, symbol, task, query, lang, source")
	fs.StringVar(&cfg.jsonOut, "json", "", "write the report JSON here")
	fs.StringVar(&cfg.dumpOut, "dump", "", "write every pair's candidate set here for training")
	fs.IntVar(&cfg.limit, "limit", 0, "measure at most this many pairs")
	fs.BoolVar(&cfg.noEngine, "no-engine", false, "attach no engine: every call abstains and the dump carries the lexical candidates")
	fs.Float64Var(&cfg.weight, "weight", -1, "override the site's blend weight")
	fs.IntVar(&cfg.k, "k", 0, "override the site's max_candidates")
	fs.IntVar(&cfg.chunk, "chunk", 0, "override the site's chunk")
	fs.DurationVar(&cfg.deadline, "deadline", 0, "override the site's deadline")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg.site = decide.Site(*site)
	if !decide.KnownSite(*site) || cfg.repo == "" || cfg.name == "" || cfg.units == "" || cfg.pairs == "" {
		return errors.New("eval needs --site, --repo, --name, --units, and --pairs")
	}
	rep, rows, err := evaluate(ctx, cfg)
	if err != nil {
		return err
	}
	printReport(rep)
	if cfg.jsonOut != "" {
		if err := writeJSON(cfg.jsonOut, rep); err != nil {
			return err
		}
	}
	if cfg.dumpOut != "" {
		if err := writeJSONL(cfg.dumpOut, rows); err != nil {
			return err
		}
	}
	return nil
}

func evaluate(ctx context.Context, cfg evalConfig) (*report, []dumpRow, error) {
	corpus, err := loadCorpus(cfg.units, cfg.name, cfg.repo)
	if err != nil {
		return nil, nil, err
	}
	var pairs []pair
	if err := readJSONL(cfg.pairs, &pairs); err != nil {
		return nil, nil, err
	}
	pairs = pairsFor(pairs, cfg.name, cfg.limit)
	if len(pairs) == 0 {
		return nil, nil, fmt.Errorf("no pairs for repository %q", cfg.name)
	}
	rr, engine, closeEngine, err := reranker(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	defer closeEngine()
	driver, err := newSiteDriver(ctx, cfg.site, corpus)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = driver.close(context.WithoutCancel(ctx)) }()

	rep := &report{Site: cfg.site, Repo: cfg.name, Engine: engine, Policy: rr.Policies.For(cfg.site), Skipped: map[string]int{}, Abstained: map[string]int{}, StartedAt: time.Now()}
	var rows []dumpRow
	var lexicalRanks, blendedRanks []int
	var elapsed []int64
	for i, p := range pairs {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		var seen []decide.Observation
		rr.Observe = func(o decide.Observation) {
			if o.Site == cfg.site {
				seen = append(seen, o)
			}
		}
		err := driver.run(ctx, rr, corpus, p)
		var skip errSkip
		switch {
		case errors.As(err, &skip):
			rep.Skipped[string(skip)]++
			continue
		case err != nil:
			rep.Skipped["error: "+err.Error()]++
			continue
		case len(seen) == 0:
			rep.Skipped[errNoObservation.Error()]++
			continue
		}
		obs := seen[0]
		target := targetIndex(cfg.site, obs.Texts, p)
		rows = append(rows, dumpFor(cfg.site, p, obs, target))
		if target < 0 {
			rep.Missing++
			continue
		}
		out := pairOutcome{
			File: p.File, Symbol: p.Symbol, Task: p.Task, Candidates: len(obs.Texts), Scored: obs.Outcome.Scored,
			LexicalRank: rankOf(obs.Lexical, target), BlendedRank: rankOf(obs.Blended, target),
			Abstained: obs.Outcome.Abstained, Reason: obs.Outcome.Reason, ElapsedMS: obs.Outcome.Elapsed.Milliseconds(),
		}
		rep.Outcomes = append(rep.Outcomes, out)
		lexicalRanks = append(lexicalRanks, out.LexicalRank)
		blendedRanks = append(blendedRanks, out.BlendedRank)
		if out.Abstained {
			rep.Abstained[out.Reason]++
		} else {
			elapsed = append(elapsed, out.ElapsedMS)
		}
		switch {
		case out.BlendedRank < out.LexicalRank:
			rep.Improved++
		case out.BlendedRank > out.LexicalRank:
			rep.Regressed++
		default:
			rep.Unchanged++
		}
		if (i+1)%25 == 0 {
			fmt.Fprintf(os.Stderr, "  %d/%d pairs\n", i+1, len(pairs))
		}
	}
	rep.Pairs = len(rep.Outcomes)
	rep.Lexical = metricsOf(lexicalRanks)
	rep.Blended = metricsOf(blendedRanks)
	rep.Latency = latencyOf(elapsed)
	rep.FinishedAt = time.Now()
	return rep, rows, nil
}

// reranker builds the site's reranker from the shipped catalog, the command
// line overrides, and a warmed engine.
func reranker(ctx context.Context, cfg evalConfig) (decide.Reranker, decide.Engine, func(), error) {
	policies, err := decide.LoadPolicies()
	if err != nil {
		return decide.Reranker{}, decide.Engine{}, nil, err
	}
	policy := policies.For(cfg.site)
	// The eval always asks; a site the catalog disables is still measured.
	policy.Enabled = true
	if cfg.weight >= 0 {
		policy.Weight = cfg.weight
	}
	if cfg.k > 0 {
		policy.MaxCandidates = cfg.k
	}
	if cfg.chunk > 0 {
		policy.Chunk = cfg.chunk
	}
	if cfg.deadline > 0 {
		policy.Deadline = cfg.deadline
	}
	policies[cfg.site] = policy
	if cfg.noEngine {
		// No decider: every call abstains, and the observer still records the
		// site's candidates and lexical scores for the dump.
		return decide.Reranker{Policies: policies}, decide.Engine{}, func() {}, nil
	}
	client := bialy.New(bialy.ConfigFromEnvironment())
	if !client.Available() {
		return decide.Reranker{}, decide.Engine{}, nil, errors.New("no engine launcher resolves; set " + bialy.EnvBinary)
	}
	fmt.Fprintln(os.Stderr, "warming the decision engine")
	if err := client.Warm(ctx); err != nil {
		return decide.Reranker{}, decide.Engine{}, nil, fmt.Errorf("warm engine: %w", err)
	}
	engine := client.Engine()
	fmt.Fprintf(os.Stderr, "engine %s model=%s device=%s head=%s\n", engine.Name, engine.Model, engine.Device, engine.Head)
	return decide.Reranker{Decider: client, Policies: policies}, engine, func() { _ = client.Close() }, nil
}

func pairsFor(all []pair, repo string, limit int) []pair {
	var out []pair
	for _, p := range all {
		if p.Repo != repo {
			continue
		}
		out = append(out, p)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func dumpFor(site decide.Site, p pair, obs decide.Observation, target int) dumpRow {
	row := dumpRow{Site: site, Repo: p.Repo, Task: p.Task, Query: p.Query, File: p.File, Symbol: p.Symbol, Line: p.Line, Lang: p.Lang}
	for i, text := range obs.Texts {
		ref := parseCandidate(text)
		c := dumpCandidate{Text: text, File: ref.File, Symbol: ref.Symbol, Line: ref.Line, Lexical: obs.Lexical[i], Target: i == target}
		if unit, ok := obs.Unit[i]; ok {
			c.Unit = &unit
		}
		row.Candidates = append(row.Candidates, c)
	}
	return row
}

// rankOf is the target's 1-based position under a stable descending order,
// the same order every site applies to its scores.
func rankOf(scores []float64, target int) int {
	rank := 1
	for i, s := range scores {
		if s > scores[target] || (s == scores[target] && i < target) {
			rank++
		}
	}
	return rank
}

func metricsOf(ranks []int) rankMetrics {
	if len(ranks) == 0 {
		return rankMetrics{}
	}
	var m rankMetrics
	for _, r := range ranks {
		m.MRR += 1 / float64(r)
		if r <= 10 {
			m.NDCG10 += 1 / math.Log2(float64(r)+1)
			m.Hit10++
		}
		if r <= 5 {
			m.Hit5++
		}
		if r == 1 {
			m.Hit1++
		}
	}
	n := float64(len(ranks))
	return rankMetrics{MRR: m.MRR / n, NDCG10: m.NDCG10 / n, Hit1: m.Hit1 / n, Hit5: m.Hit5 / n, Hit10: m.Hit10 / n}
}

func latencyOf(ms []int64) latency {
	if len(ms) == 0 {
		return latency{}
	}
	sorted := append([]int64(nil), ms...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	at := func(q float64) int64 { return sorted[min(len(sorted)-1, int(math.Ceil(q*float64(len(sorted))))-1)] }
	return latency{P50MS: at(0.5), P95MS: at(0.95), MaxMS: sorted[len(sorted)-1]}
}

func printReport(r *report) {
	fmt.Printf("site %s on %s: %d pairs measured, %d target missing\n", r.Site, r.Repo, r.Pairs, r.Missing)
	if r.Engine.Name != "" {
		fmt.Printf("engine %s model=%s device=%s head=%s\n", r.Engine.Name, r.Engine.Model, r.Engine.Device, r.Engine.Head)
	}
	fmt.Printf("policy weight=%.2f k=%d chunk=%d deadline=%s\n", r.Policy.Weight, r.Policy.MaxCandidates, r.Policy.Chunk, r.Policy.Deadline)
	fmt.Printf("%-10s %8s %8s %8s %8s %8s\n", "", "mrr", "ndcg@10", "hit@1", "hit@5", "hit@10")
	fmt.Printf("%-10s %8.3f %8.3f %8.3f %8.3f %8.3f\n", "lexical", r.Lexical.MRR, r.Lexical.NDCG10, r.Lexical.Hit1, r.Lexical.Hit5, r.Lexical.Hit10)
	fmt.Printf("%-10s %8.3f %8.3f %8.3f %8.3f %8.3f\n", "blended", r.Blended.MRR, r.Blended.NDCG10, r.Blended.Hit1, r.Blended.Hit5, r.Blended.Hit10)
	fmt.Printf("improved %d, regressed %d, unchanged %d\n", r.Improved, r.Regressed, r.Unchanged)
	fmt.Printf("latency p50=%dms p95=%dms max=%dms\n", r.Latency.P50MS, r.Latency.P95MS, r.Latency.MaxMS)
	for reason, n := range r.Abstained {
		fmt.Printf("abstained %d: %s\n", n, reason)
	}
	for reason, n := range r.Skipped {
		fmt.Printf("skipped %d: %s\n", n, reason)
	}
}

func writeJSON(name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(name, data, 0o600)
}
