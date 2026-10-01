package secretmatch

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// bodyWith surrounds text with credential-free prose.
func bodyWith(text string) string {
	return strings.Repeat("the coordinator surveyed the workspace and cited paths\n", 40) +
		text + "\n" + strings.Repeat("no credential appears in this prose\n", 40)
}

func TestScreenCacheDoesNotChangeResults(t *testing.T) {
	ctx := context.Background()
	cached := loadBundled(t)
	uncached := loadBundled(t)
	uncached.screenCache = nil

	for name, body := range map[string]string{
		"aws":     bodyWith("aws_key = " + plantAWS),
		"github":  bodyWith("gh = " + plantGitHub),
		"nothing": bodyWith("port = 8080"),
	} {
		want := dedupeAndSort(uncached.screenRaw(ctx, body))
		// Twice: the first screen fills the cache, the second reads it.
		for pass := range 2 {
			got := dedupeAndSort(cached.screenRaw(ctx, body))
			if len(got) != len(want) {
				t.Fatalf("%s pass %d: %d hits, want %d", name, pass, len(got), len(want))
			}
			for i := range got {
				if got[i].RuleID != want[i].RuleID || got[i].Start != want[i].Start || got[i].End != want[i].End {
					t.Errorf("%s pass %d hit %d: %+v, want %+v", name, pass, i, got[i], want[i])
				}
			}
		}
	}
}

// Harvest evidence is applied after catalog-cache lookup.
func TestHarvestFoundInABodyAlreadyScreened(t *testing.T) {
	ctx := context.Background()
	m := loadBundled(t)
	body := bodyWith("db_dsn = postgres://svc:hunter2-not-a-shape@db.internal/app")

	if hits := m.screenRaw(ctx, body); len(hits) != 0 {
		t.Fatalf("body matched %d rule(s) before harvest; pick a value with no shape", len(hits))
	}

	// Add evidence after the catalog result is cached.
	m.SetHarvestSource(func(context.Context) []HarvestedValue {
		return []HarvestedValue{asContainerValue(HarvestedValue{
			Name: "DB_PASSWORD", Container: ".env", Secret: "hunter2-not-a-shape",
		})}
	})

	hits := m.screenRaw(ctx, body)
	if len(hits) == 0 {
		t.Fatal("a value harvested after the body was screened was not found — the cache outlived its evidence")
	}
	if hits[0].RuleID != HarvestRuleID {
		t.Errorf("rule = %q, want %q", hits[0].RuleID, HarvestRuleID)
	}
	if out, spans := redactMatches(body, hits); len(spans) != 1 || strings.Contains(out, "hunter2-not-a-shape") {
		t.Errorf("redaction n=%d left the value: %s", len(spans), out)
	}
}

// Suppression is applied after catalog-cache lookup.
func TestSuppressionAppliesToABodyAlreadyScreened(t *testing.T) {
	ctx := context.Background()
	m := loadBundled(t)
	// Suppression keys on the fingerprint, which is empty without one.
	fingerprinter, err := NewFingerprinter([]byte(strings.Repeat("k", fingerprintKeyBytes)))
	testutil.FailErr(t, "new fingerprinter", err)
	m.SetFingerprinter(fingerprinter)
	body := bodyWith("aws_key = " + plantAWS)

	before := m.screenRaw(ctx, body)
	if len(before) == 0 {
		t.Fatal("planted AWS key not detected")
	}
	fp := before[0].Fingerprint
	if fp == "" {
		t.Fatal("hit carries no fingerprint; suppression cannot key on it")
	}

	m.SetIgnoredSource(func(context.Context) map[SecretFingerprint]bool {
		return map[SecretFingerprint]bool{fp: true}
	})
	for _, hit := range m.screenRaw(ctx, body) {
		if hit.Fingerprint == fp {
			t.Fatal("an ignored fingerprint still matched from cache")
		}
	}
}

func TestScreenCacheIncludesShortBodies(t *testing.T) {
	ctx := context.Background()
	m := loadBundled(t)
	short := "aws_key = " + plantAWS
	m.screenRaw(ctx, short)
	if got := len(m.screenCache.entries); got != 1 {
		t.Errorf("cache holds %d entr(ies) for a short field, want 1", got)
	}
}

// Eviction depends only on cache state.
func TestScreenCacheEvictsOldestBeyondItsBound(t *testing.T) {
	c := newScreenCache()
	first := screenCacheKeyFor("body 0")
	for i := range maxScreenCacheEntries + 64 {
		c.put(screenCacheKeyFor("body "+itoa(i)), []Match{{RuleID: "r", Start: i, End: i + 1}})
	}
	if got := len(c.entries); got > maxScreenCacheEntries {
		t.Errorf("cache holds %d entries, want <= %d", got, maxScreenCacheEntries)
	}
	if got := len(c.order); got != len(c.entries) {
		t.Errorf("order (%d) and entries (%d) disagree", got, len(c.entries))
	}
	if _, ok := c.get(first); ok {
		t.Error("the oldest entry survived eviction")
	}
	if _, ok := c.get(screenCacheKeyFor("body " + itoa(maxScreenCacheEntries+63))); !ok {
		t.Error("the newest entry was evicted")
	}
	if c.matches != len(c.entries) {
		t.Errorf("match accounting = %d, want %d", c.matches, len(c.entries))
	}
}

// Oversized results do not displace existing entries.
func TestScreenCacheRefusesAnOversizedResult(t *testing.T) {
	c := newScreenCache()
	c.put(screenCacheKeyFor("small"), []Match{{RuleID: "r"}})
	c.put(screenCacheKeyFor("huge"), make([]Match, maxScreenCacheMatches+1))
	if _, ok := c.get(screenCacheKeyFor("huge")); ok {
		t.Error("an oversized result was cached")
	}
	if _, ok := c.get(screenCacheKeyFor("small")); !ok {
		t.Error("the oversized put evicted an existing entry")
	}
}

// Canceled passes are partial and are not cached.
func TestCanceledScreenIsNotCached(t *testing.T) {
	m := loadBundled(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.screenRaw(ctx, bodyWith("aws_key = "+plantAWS))
	if got := len(m.screenCache.entries); got != 0 {
		t.Errorf("cache holds %d entr(ies) from a canceled screen, want 0", got)
	}
}

// Cached spans are copied for each caller.
func TestCachedHitsAreCopiedPerCaller(t *testing.T) {
	ctx := context.Background()
	m := loadBundled(t)
	body := bodyWith("aws_key = " + plantAWS)

	first := m.screenRaw(ctx, body)
	if len(first) == 0 {
		t.Fatal("planted AWS key not detected")
	}
	firstStart := first[0].Start
	first[0].Start = -999

	second := m.screenRaw(ctx, body)
	if second[0].Start != firstStart {
		t.Errorf("second caller saw Start=%d after the first mutated its slice; want %d", second[0].Start, firstStart)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
