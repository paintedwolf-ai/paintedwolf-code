package secretmatch

import (
	"context"
	"strings"
	"testing"
)

func harvestMatcher(t *testing.T, values ...HarvestedValue) *Matcher {
	t.Helper()
	for i := range values {
		values[i] = asContainerValue(values[i])
	}
	return matcherWithHarvest(t, values...)
}

// matcherWithHarvest keeps each value's own evidence identity.
func matcherWithHarvest(t *testing.T, values ...HarvestedValue) *Matcher {
	t.Helper()
	m, err := BuildMatcher(Bundled())
	if err != nil {
		t.Fatalf("BuildMatcher: %v", err)
	}
	fingerprinter, err := NewFingerprinter([]byte(strings.Repeat("h", 32)))
	if err != nil {
		t.Fatalf("NewFingerprinter: %v", err)
	}
	m.SetFingerprinter(fingerprinter)
	m.SetHarvestSource(func(context.Context) []HarvestedValue {
		return append([]HarvestedValue(nil), values...)
	})
	return m
}

// Exact-match evidence still needs approval-safe shape metadata.
func TestHarvestMatchCarriesAGenericShape(t *testing.T) {
	const planted = "b7Qk2wRt9YzE4pLm"
	m := harvestMatcher(t, HarvestedValue{
		Name: "MYSQL_PASSWORD", Container: ".env", Secret: planted,
		Fingerprint: SecretFingerprint("sf1_harvest"),
	})
	hits := m.ScreenContext(context.Background(), "connecting with "+planted)
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want the harvested value matched once", len(hits))
	}
	hit := hits[0]
	if hit.RuleID != HarvestRuleID || hit.VarName != "MYSQL_PASSWORD" || hit.Container != ".env" {
		t.Fatalf("harvest evidence = %+v", hit)
	}
	if strings.TrimSpace(hit.GenericShape) == "" {
		t.Fatal("harvest match has no generic shape, so no card can be compiled from it")
	}
	if strings.Contains(hit.GenericShape, planted) {
		t.Fatalf("generic shape leaked the matched value: %q", hit.GenericShape)
	}
	// The shape preserves only size and character classes.
	if !strings.Contains(hit.GenericShape, "16 characters") {
		t.Fatalf("generic shape = %q, want a synthetic example sized like the value", hit.GenericShape)
	}
}

// Every evidence source produces card-ready matches.
func TestEveryMatchSourceProducesACardReadyMatch(t *testing.T) {
	const planted = "AKIAQYJK5TXV4NZR7SGB"
	m := harvestMatcher(t, HarvestedValue{
		Name: "TOKEN", Container: ".env.local", Secret: "q4Wv8ZbN2mKx7Lp3",
		Fingerprint: SecretFingerprint("sf1_harvest"),
	})
	hits := m.ScreenContext(context.Background(), "key="+planted+" pass=q4Wv8ZbN2mKx7Lp3")
	if len(hits) < 2 {
		t.Fatalf("hits = %d, want both a rule hit and a harvest hit", len(hits))
	}
	for _, hit := range hits {
		if strings.TrimSpace(hit.GenericShape) == "" {
			t.Fatalf("match from %q has no generic shape: %+v", hit.RuleID, hit)
		}
		if strings.TrimSpace(string(hit.Fingerprint)) == "" {
			t.Fatalf("match from %q has no fingerprint: %+v", hit.RuleID, hit)
		}
	}
}

// Declared values use the managed exact-match floor.
func TestManagedSecretBelowTheHarvestedFloorStillMatches(t *testing.T) {
	const planted = "Ky7-Lp" // MinManagedSecretRunes, under MinHarvestNeedleRunes
	managed := matcherWithHarvest(t, HarvestedValue{
		Name: "deploy key", Secret: planted, Fingerprint: SecretFingerprint("sf1_managed"),
		RuleID: ManagedRuleID, Title: ManagedRuleTitle,
		Source: SourceRememberedMatch, NonDisclosable: true,
	})
	hits := managed.ScreenContext(context.Background(), "wrote "+planted+" to the log")
	if len(hits) != 1 || !hits[0].NonDisclosable || hits[0].RuleID != ManagedRuleID {
		t.Fatalf("managed value of %d runes was matched by nothing: %+v",
			len([]rune(planted)), hits)
	}
	// Guessed values use the stricter harvested floor.
	guess := harvestMatcher(t, HarvestedValue{Name: "MODE", Container: ".env", Secret: planted})
	if noisy := guess.ScreenContext(context.Background(), "wrote "+planted+" to the log"); len(noisy) != 0 {
		t.Fatalf("harvested guess matched below its floor: %+v", noisy)
	}
}

func TestNonDisclosableHarvestOverridesPublicValueIgnore(t *testing.T) {
	const planted = "host-generated-value-q4Wv8ZbN2mKx7Lp3"
	fingerprint := SecretFingerprint("sf1_generated")
	m := harvestMatcher(t, HarvestedValue{
		Name: "signing key", Secret: planted, Fingerprint: fingerprint,
		RuleID: "managed-secret", Title: "A managed secret",
		Source: SourceRememberedMatch, NonDisclosable: true,
	})
	m.SetIgnoredSource(func(context.Context) map[SecretFingerprint]bool {
		return map[SecretFingerprint]bool{fingerprint: true}
	})
	hits := m.ScreenContext(context.Background(), "prefix"+planted+"suffix")
	if len(hits) != 1 || !hits[0].NonDisclosable {
		t.Fatalf("embedded non-disclosable hit was suppressed: %+v", hits)
	}
	repeated := m.ScreenContext(context.Background(), strings.Repeat(planted+".", maxHarvestOccurrences+1))
	if len(repeated) != maxHarvestOccurrences+1 {
		t.Fatalf("non-disclosable hits were capped: got %d", len(repeated))
	}
	ordinary := harvestMatcher(t, HarvestedValue{
		Name: "signing key", Secret: planted, Fingerprint: fingerprint,
	})
	if hits := ordinary.ScreenContext(context.Background(), "prefix"+planted+"suffix"); len(hits) != 0 {
		t.Fatalf("ordinary embedded value produced noisy hits: %+v", hits)
	}
}

// Admitted four-digit values remain eligible for exact-match screening.
func TestDeclaredEvidenceMatchesAFourDigitPIN(t *testing.T) {
	if MinNeedleRunes(true) > 4 {
		t.Fatalf("declared floor = %d, want at most 4 so a PIN is screened", MinNeedleRunes(true))
	}
	if MinNeedleRunes(false) < MinNeedleRunes(true) {
		t.Fatal("guessed evidence must not have a lower floor than declared evidence")
	}
}
