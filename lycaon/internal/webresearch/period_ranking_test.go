package webresearch

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func mustPeriod(t *testing.T, raw string) Period {
	t.Helper()
	p, err := ParsePeriod(raw)
	testutil.FailErr(t, "parse period "+raw, err)
	return p
}

func TestQueryYearDoesNotEarnTheWindowBonus(t *testing.T) {
	scorer := newQueryScorer("enterprise design trends 2025", nil, false, CurrentPeriod())
	stale := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	if scorer.periodMatch(stale) {
		t.Fatal("a year in the query text must not count as a declared window")
	}
}

func TestDeclaredWindowEarnsTheBonus(t *testing.T) {
	scorer := newQueryScorer("payment outage postmortem", nil, false, mustPeriod(t, "2025"))
	if !scorer.periodMatch(time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("a page inside the declared window must earn the bonus")
	}
	if scorer.periodMatch(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("a page outside the declared window must not earn the bonus")
	}
}

func TestHistoricalWindowSilencesRecency(t *testing.T) {
	now := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	recent := now.AddDate(0, 0, -10)

	current := newQueryScorer("payment outage postmortem", nil, true, CurrentPeriod())
	current.now = now
	if current.recencyBoost(recent) <= 0 {
		t.Fatal("current window must still reward recent pages")
	}

	historical := newQueryScorer("payment outage postmortem", nil, true, mustPeriod(t, "2025"))
	historical.now = now
	if got := historical.recencyBoost(recent); got != 0 {
		t.Fatalf("historical window recencyBoost = %v, want 0", got)
	}
}

func TestHistoricalWindowRanksItsOwnYearFirst(t *testing.T) {
	now := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	scorer := newQueryScorer("payment outage postmortem", nil, true, mustPeriod(t, "2025"))
	scorer.now = now

	inWindow := indexCandidate{
		URL: "https://example.com/2025", Title: "payment outage postmortem",
		Date: time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC),
	}
	fresher := indexCandidate{
		URL: "https://example.com/2026", Title: "payment outage postmortem",
		Date: now.AddDate(0, 0, -5),
	}
	if scorer.rankCandidate(inWindow) <= scorer.rankCandidate(fresher) {
		t.Fatal("declared window must rank its own year above a fresher page")
	}
}

func TestSeedFreshnessYieldsToDeclaredWindow(t *testing.T) {
	plan := seedPlan{fresh: true}
	if !plan.freshFor(CurrentPeriod()) {
		t.Fatal("current window must keep the seed model's freshness call")
	}
	if plan.freshFor(mustPeriod(t, "2025")) {
		t.Fatal("declared past window must clear freshness")
	}
}

func TestDateOutsideFreshWindowUsesDateEvidence(t *testing.T) {
	now := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	candidate := indexCandidate{URL: "https://example.com/p", Title: "widget 2023 edition", Date: time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)}

	current := newQueryScorer("widget review 2025", nil, true, CurrentPeriod())
	current.now = now
	if !dateOutsideFreshWindow(candidate, pageProbe{}, current) {
		t.Fatal("a page dated 2023 is outside a current-window search in 2026")
	}

	declared := newQueryScorer("widget review", nil, true, mustPeriod(t, "2024"))
	declared.now = now
	if dateOutsideFreshWindow(candidate, pageProbe{}, declared) {
		t.Fatal("a page dated 2023 is adjacent to a declared 2024 window, not obsolete")
	}
	candidate.Date = time.Time{}
	if dateOutsideFreshWindow(candidate, pageProbe{}, current) {
		t.Fatal("a title year without date provenance must not exclude the page")
	}
}

func TestSubjectYearStaysLexicallySearchable(t *testing.T) {
	scorer := newQueryScorer("CVE-2025-1234 mitigation", nil, false, CurrentPeriod())
	found := false
	for _, term := range scorer.terms {
		if term == "2025" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected 2025 among lexical terms, got %v", scorer.terms)
	}
}

func TestSearchCachesPerWindow(t *testing.T) {
	base := Settings{PerProviderTimeoutSec: 5}
	current := normalizeSearchResultCacheKey("payment outage", CurrentPeriod(), 10, base, "")
	historical := normalizeSearchResultCacheKey("payment outage", mustPeriod(t, "2025"), 10, base, "")
	if current == historical {
		t.Fatal("period must be part of the whole-search cache key")
	}
	planCurrent := normalizeSeedPlanCacheKey("payment outage", CurrentPeriod(), "")
	planHistorical := normalizeSeedPlanCacheKey("payment outage", mustPeriod(t, "2025"), "")
	if planCurrent == planHistorical {
		t.Fatal("period must be part of the seed-plan cache key")
	}
}
