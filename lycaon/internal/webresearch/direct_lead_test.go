package webresearch

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLeadTitleNGramsAdjacentPairs(t *testing.T) {
	grams := leadTitleNGrams("Steam Machine 2026: Is Valve's living room PC finally worth buying?")
	for _, want := range []string{"steam machine", "living room", "worth buying"} {
		if !slices.Contains(grams, want) {
			t.Fatalf("grams = %v missing %q", grams, want)
		}
	}
	if len(grams) > maxLeadTitleNGrams {
		t.Fatalf("grams = %d want <= %d", len(grams), maxLeadTitleNGrams)
	}
}

func TestKeepDiscriminatingPhrasesDropsCorpusWidePhrases(t *testing.T) {
	texts := []string{
		"is the steam machine worth it",
		"is the best controller of 2026",
		"is the new dock any good",
		"is the price fair",
		"is the fan loud",
		"is the os stable",
		"is the library ready",
		"steam machine review benchmarks",
	}
	kept := keepDiscriminatingPhrases([]string{"is the", "steam machine"}, texts)
	if slices.Contains(kept, "is the") {
		t.Fatalf("kept = %v want corpus-wide phrase dropped", kept)
	}
	if !slices.Contains(kept, "steam machine") {
		t.Fatalf("kept = %v want rare phrase kept", kept)
	}
}

func TestKeepDiscriminatingPhrasesSmallCorpusKeepsAll(t *testing.T) {
	texts := []string{"is the a", "is the b", "is the c"}
	kept := keepDiscriminatingPhrases([]string{"is the"}, texts)
	if len(kept) != 1 {
		t.Fatalf("kept = %v want all phrases below corpus floor", kept)
	}
}

func TestLeadTitleNGramsShortTitleKeptWhole(t *testing.T) {
	grams := leadTitleNGrams("Steam Machine review benchmarks")
	if !slices.Contains(grams, "steam machine review benchmarks") {
		t.Fatalf("grams = %v want whole short title", grams)
	}
	if !slices.Contains(grams, "review benchmarks") {
		t.Fatalf("grams = %v want bigram", grams)
	}
}

func TestLeadTitleNGramsEmptyAndJunk(t *testing.T) {
	if grams := leadTitleNGrams(""); len(grams) != 0 {
		t.Fatalf("grams = %v want none", grams)
	}
	if grams := leadTitleNGrams("a I"); len(grams) != 0 {
		t.Fatalf("grams = %v want none for sub-2-char tokens", grams)
	}
}

func TestCrawlPhrasesIncludeLeadNGramsButRankPhrasesDoNot(t *testing.T) {
	plan := seedPlan{
		expand: []string{"Valve Steam Machine"},
		leads: []seedLead{
			{title: "Steam Machine 2026: Is Valve's living room PC finally worth buying?", host: "theverge.com"},
		},
	}
	query := "steam machine reviews"
	crawl := plan.crawlPhrases(query)
	if !slices.Contains(crawl, "living room") {
		t.Fatalf("crawlPhrases = %v missing lead n-gram", crawl)
	}
	strict := plan.rankPhrases(query)
	if slices.Contains(strict, "living room") {
		t.Fatalf("rankPhrases = %v must not contain lead n-grams", strict)
	}
	// Both keep the full headline and expand phrases.
	for _, set := range [][]string{crawl, strict} {
		if !slices.Contains(set, "valve steam machine") {
			t.Fatalf("phrases = %v missing expand", set)
		}
	}
}

// failThenRespondSummarizer errors the first call and answers later ones,
// recording each user message.
type failThenRespondSummarizer struct {
	response string
	calls    []string
	failed   bool
}

func (s *failThenRespondSummarizer) Summarize(_ context.Context, _, user string, _ int) (string, error) {
	s.calls = append(s.calls, user)
	if !s.failed {
		s.failed = true
		return "", context.DeadlineExceeded
	}
	return s.response, nil
}

func TestPickSeedsSlimRetryAfterTimeout(t *testing.T) {
	testutil.SkipIfShort(t, "seed pick slim retry after timeout")
	resetDirectState(1)
	sum := &failThenRespondSummarizer{
		response: `{"seeds":["https://docs.example.com"],"fresh":false,"expand":["widget install"],"leads":[{"title":"Widget install guide","host":"docs.example.com"}]}`,
	}
	d := &directDiscoverer{summarizer: sum, seedSharedProvider: true}
	plan, err := d.pickSeedsCached(context.Background(), "widget install guide", CurrentPeriod(), 8, nil, nil)
	if err != nil {
		t.Fatalf("pickSeedsCached: %v", err)
	}
	if len(plan.seeds) == 0 {
		t.Fatalf("plan = %+v want seeds from retry", plan)
	}
	if len(sum.calls) != 2 {
		t.Fatalf("calls = %d want full call + slim retry", len(sum.calls))
	}
	slim := slimSeedBudget(8)
	wantAsk := fmt.Sprintf("Return %d-%d leads", slim.minLeads, slim.maxLeads)
	if !strings.Contains(sum.calls[1], wantAsk) {
		t.Fatalf("retry ask = %q want slim budget %q", sum.calls[1], wantAsk)
	}
}

func TestPickSeedsNoRetryWhenCallerContextDone(t *testing.T) {
	resetDirectState(1)
	sum := &failThenRespondSummarizer{response: `{"seeds":["https://docs.example.com"]}`}
	d := &directDiscoverer{summarizer: sum}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.pickSeedsCached(ctx, "widget install guide", CurrentPeriod(), 8, nil, nil); err == nil {
		t.Fatal("want error when caller context is done")
	}
	// The seed-call queue refuses dead contexts before the model is reached,
	// and pickSeedsCached must not slim-retry a canceled caller either.
	if len(sum.calls) != 0 {
		t.Fatalf("calls = %d want none after caller cancellation", len(sum.calls))
	}
}

func TestRankSiteLinksLeadNGramLiftsSlugMatch(t *testing.T) {
	plan := seedPlan{
		leads: []seedLead{{title: "The Steam Machine review: living room gaming at last", host: "theverge.com"}},
	}
	phrases := plan.crawlPhrases("valve console verdict")
	links := []siteIndexLink{
		{URL: "https://www.theverge.com/tech/celebrity-gadget-tour", Title: "celebrity gadget tour"},
		{URL: "https://www.theverge.com/reviews/steam-machine-review", Title: "steam machine review"},
	}
	ranked := rankSiteLinks(links, phrases)
	if ranked[0].URL != "https://www.theverge.com/reviews/steam-machine-review" {
		t.Fatalf("ranked[0] = %s want lead-matching slug first", ranked[0].URL)
	}
}
