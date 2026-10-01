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
