package secretmatch

import (
	"context"
	"strings"
	"testing"
)

const testReference = "{{paintedwolf-secret:018ff2db-85f7-7f31-8da2-b9e81cd1a150}}"

func managedValue(secret, reference string) HarvestedValue {
	return HarvestedValue{
		Name: "deploy token", Container: "managed secret", Secret: secret,
		RuleID: ManagedRuleID, Title: ManagedRuleTitle, Source: SourceRememberedMatch,
		Reference: reference, NonDisclosable: true,
	}
}

func matcherWith(t *testing.T, values ...HarvestedValue) *Matcher {
	t.Helper()
	m := loadBundled(t)
	m.SetHarvestSource(func(context.Context) []HarvestedValue { return values })
	return m
}

func keepNone(Match) bool { return false }

func TestProjectionWritesALiveManagedValueAsItsReference(t *testing.T) {
	const secret = "orchard-protected-value-7731"
	m := matcherWith(t, managedValue(secret, testReference))
	in := "STRIPE_KEY=" + secret + "\nPORT=3000"

	out, written := m.ProjectLabeledWhere(context.Background(), "", in, keepNone)

	if want := "STRIPE_KEY=" + testReference + "\nPORT=3000"; out != want {
		t.Fatalf("projection = %q, want %q", out, want)
	}
	if len(written) != 1 {
		t.Fatalf("replacements = %+v, want one", written)
	}
	got := written[0]
	start := len([]rune("STRIPE_KEY="))
	if got.Reference != testReference || got.Start != start || got.Length != len([]rune(testReference)) {
		t.Fatalf("marker = %+v, want the reference at %d", got, start)
	}
	if got.SourceStart != start || got.SourceEnd != start+len([]rune(secret)) {
		t.Fatalf("source range = [%d,%d), want the value's range", got.SourceStart, got.SourceEnd)
	}
	if got.RuleID != ManagedRuleID {
		t.Fatalf("attribution = %q, want the managed rule", got.RuleID)
	}
}

func TestProjectionWritesOneReferenceForAMultiLineValue(t *testing.T) {
	const secret = "MIIEowIBAAKC\nAQEAz9Kj4Lp2\nQhVn8sWtYbXc"
	m := matcherWith(t, managedValue(secret, testReference))

	out, written := m.ProjectLabeledWhere(context.Background(), "", "key:\n"+secret+"\nend", keepNone)

	if out != "key:\n"+testReference+"\nend" || len(written) != 1 {
		t.Fatalf("projection = %q with %d replacements, want one unsplit reference", out, len(written))
	}
}

// A numbered read splits the value into lines, which are not the referenced value.
func TestProjectionNeverReferencesOneLineOfAValue(t *testing.T) {
	const secret = "MIIEowIBAAKC\nAQEAz9Kj4Lp2\nQhVn8sWtYbXc"
	m := matcherWith(t, managedValue(secret, testReference))
	in := "1\tMIIEowIBAAKC\n2\tAQEAz9Kj4Lp2\n3\tQhVn8sWtYbXc"

	if out, _ := m.ProjectLabeledWhere(context.Background(), "", in, keepNone); out != in {
		t.Fatalf("a line of the value was referenced rather than left for the screen: %q", out)
	}
	out, written := m.ProjectLabeledWhere(context.Background(), "", in, nil)

	if strings.Contains(out, testReference) {
		t.Fatalf("a single line was written as the whole value's reference: %q", out)
	}
	for _, line := range strings.Split(secret, "\n") {
		if strings.Contains(out, line) {
			t.Fatalf("protected line %q survived: %q", line, out)
		}
	}
	for _, replacement := range written {
		if replacement.Reference != "" {
			t.Fatalf("replacement = %+v, want placeholders only", replacement)
		}
	}
}

// A broader match loses its extra bytes; the protected value keeps its reference.
func TestProjectionRemovesWhatABroaderMatchClaimsAroundAReference(t *testing.T) {
	const secret = "inner-protected-value-4410"
	const broader = "outer-" + secret + "-tail"
	m := matcherWith(t,
		managedValue(secret, testReference),
		asContainerValue(HarvestedValue{Name: "TOKEN", Container: ".env", Secret: broader}),
	)

	out, written := m.ProjectLabeledWhere(context.Background(), "", "TOKEN="+broader+" ok", nil)

	if want := "TOKEN=[REDACTED]" + testReference + "[REDACTED] ok"; out != want {
		t.Fatalf("projection = %q, want %q", out, want)
	}
	if len(written) != 3 || written[1].Reference != testReference {
		t.Fatalf("replacements = %+v, want placeholder, reference, placeholder", written)
	}
	if written[0].RuleID != HarvestRuleID || written[2].RuleID != HarvestRuleID {
		t.Fatalf("remainder attribution = %q/%q, want the broader match", written[0].RuleID, written[2].RuleID)
	}
}

func TestProjectionLeavesUnkeptMatchesForTheScreen(t *testing.T) {
	const secret = "orchard-protected-value-7731"
	const retired = "orchard-retired-value-9902"
	const container = "container-harvest-value-5521"
	m := matcherWith(t,
		managedValue(secret, testReference),
		managedValue(retired, ""),
		asContainerValue(HarvestedValue{Name: "OTHER", Container: ".env", Secret: container}),
	)
	in := secret + " " + retired + " " + container

	out, _ := m.ProjectLabeledWhere(context.Background(), "", in, keepNone)

	if want := testReference + " " + retired + " " + container; out != want {
		t.Fatalf("projection = %q, want only the live value replaced", out)
	}
	redacted, _ := m.ProjectLabeledWhere(context.Background(), "", in, nil)
	if want := testReference + " [REDACTED] [REDACTED]"; redacted != want {
		t.Fatalf("keep-all projection = %q, want %q", redacted, want)
	}
}
