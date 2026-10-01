package secretmatch

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRepeatedShortFieldsMatchUncachedDetection(t *testing.T) {
	cached, uncached := loadBundled(t), loadBundled(t)
	uncached.screenCache = nil
	for _, field := range []struct{ label, value string }{
		{"description", "An ordinary tool description"},
		{"token", plantGitHub},
		{"description", "unicode 雪 " + plantAWS},
		{"password", "different-label-context-123456789"},
		{"description", "different-label-context-123456789"},
		{"", ""},
	} {
		want := uncached.ScreenLabeledContext(t.Context(), field.label, field.value)
		for range 3 {
			got := cached.ScreenLabeledContext(t.Context(), field.label, field.value)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("label %q: cached detection differs from uncached", field.label)
			}
		}
	}
}

func TestRepeatedShortFieldsKeepDynamicEvidenceIsolated(t *testing.T) {
	m := loadBundled(t)
	fingerprint, err := NewFingerprinter([]byte(strings.Repeat("k", fingerprintKeyBytes)))
	testutil.FailErr(t, "build fingerprinter", err)
	m.SetFingerprinter(fingerprint)
	const value = "short-non-pattern-fixture"
	const reference = "{{paintedwolf-secret:018ff2db-85f7-7f31-8da2-b9e81cd1a151}}"
	if hits := m.ScreenContext(t.Context(), value); len(hits) != 0 {
		t.Fatal("fixture matched before protection")
	}
	protected := WithScreeningValues(t.Context(), []Remembered{{Secret: value,
		RuleID: ManagedRuleID, Reference: reference, NonDisclosable: true}})
	for range 3 {
		hits := m.ScreenContext(protected, value)
		if len(hits) != 1 || hits[0].Reference != reference || !hits[0].NonDisclosable {
			t.Fatal("reused catalog result hid current managed evidence")
		}
		if hits := m.ScreenContext(t.Context(), value); len(hits) != 0 {
			t.Fatal("one request's managed evidence contaminated another request")
		}
	}
}
