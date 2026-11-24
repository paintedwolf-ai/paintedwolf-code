package secretmatch

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProjectCorrectionsStayExactScopedAndRevocable(t *testing.T) {
	m := loadBundled(t)
	fp, err := NewFingerprinter([]byte(strings.Repeat("k", fingerprintKeyBytes)))
	testutil.FailErr(t, "fingerprinter", err)
	m.SetFingerprinter(fp)
	active := true
	m.SetIgnoredSource(func(ctx context.Context) map[SecretFingerprint]bool {
		return map[SecretFingerprint]bool{fp.Fingerprint(plantGitHub): active && AskAttributionFrom(ctx).ProjectID == "project"}
	})
	project := WithAskAttribution(t.Context(), AskAttribution{ProjectID: "project"})
	if len(m.ScreenContext(project, plantGitHub)) != 0 {
		t.Fatal("accepted exact value still flagged")
	}
	if len(m.ScreenContext(t.Context(), plantGitHub)) == 0 {
		t.Fatal("exception escaped project")
	}
	if m.Ignored(project, plantGitHub+"suffix") {
		t.Fatal("exception became a prefix match")
	}
	revision := m.ClassificationRevision(project)
	snapshot := m.ClassificationSnapshot(project)
	active = false
	if len(m.ScreenContext(project, plantGitHub)) == 0 {
		t.Fatal("cached match retained withdrawn exception")
	}
	if m.ClassificationRevision(project) == revision || m.CheckClassification(project, snapshot) == nil {
		t.Fatal("withdrawal did not invalidate prepared release")
	}
	active = true
	protected := WithScreeningValues(project, []Remembered{{Secret: plantGitHub, RuleID: ManagedRuleID, NonDisclosable: true}})
	if hits := m.ScreenContext(protected, plantGitHub); len(hits) != 1 || !hits[0].NonDisclosable {
		t.Fatal("exception hid protected evidence")
	}
	if m.Ignored(protected, plantGitHub) {
		t.Fatal("protected credential classified as fixture")
	}
}

func TestHarvestDigestReflectsValueAndGrantChanges(t *testing.T) {
	var nilMatcher *Matcher
	if nilMatcher.HarvestDigest(t.Context()) != ([32]byte{}) {
		t.Fatal("nil matcher returned non-zero digest")
	}
	m := loadBundled(t)
	if m.HarvestDigest(t.Context()) != ([32]byte{}) {
		t.Fatal("empty harvest returned non-zero digest")
	}
	items := []HarvestedValue{
		{Name: "API_KEY", Secret: "secret-alpha-1111", Fingerprint: "fp1", RuleID: ManagedRuleID, Reference: "ref-1", NonDisclosable: true},
		{Name: "DB_PASS", Secret: "secret-bravo-2222", Fingerprint: "fp2", RuleID: HarvestRuleID},
	}
	m.SetHarvestSource(func(context.Context) []HarvestedValue { return items })
	d1 := m.HarvestDigest(t.Context())
	if d1 == ([32]byte{}) {
		t.Fatal("populated harvest returned zero digest")
	}
	// Order-independent across identical items.
	m.SetHarvestSource(func(context.Context) []HarvestedValue {
		return []HarvestedValue{items[1], items[0]}
	})
	if m.HarvestDigest(t.Context()) != d1 {
		t.Fatal("harvest digest depended on slice order")
	}
	// Retiring a grant or changing a secret changes the digest.
	retired := []HarvestedValue{
		{Name: "API_KEY", Secret: "secret-alpha-1111", Fingerprint: "fp1", RuleID: ManagedRuleID, Reference: "ref-1", Retired: true, NonDisclosable: true},
		items[1],
	}
	m.SetHarvestSource(func(context.Context) []HarvestedValue { return retired })
	if m.HarvestDigest(t.Context()) == d1 {
		t.Fatal("grant retirement did not change harvest digest")
	}
}
