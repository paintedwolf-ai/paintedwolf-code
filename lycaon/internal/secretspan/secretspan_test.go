package secretspan

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func bundledScreener(t *testing.T) *Screener {
	t.Helper()
	m, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	if err != nil {
		testutil.FailErr(t, "build bundled matcher", err)
	}
	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{0x5a}, 32))
	if err != nil {
		testutil.FailErr(t, "fingerprinter", err)
	}
	m.SetFingerprinter(fp)
	return New(m)
}

func TestScreenCompletesWithNoSpans(t *testing.T) {
	got := bundledScreener(t).Screen(context.Background(), "const answer = 42\n")
	if len(got.Spans) != 0 {
		t.Fatalf("spans = %d, want none", len(got.Spans))
	}
	if got.Truncated {
		t.Fatal("a small document is not truncated")
	}
}

func TestScreenFindsACatalogValueAndCarriesNoBytes(t *testing.T) {
	text := "awsAccessKeyId: AKIAQYJK5TXV4NZR7SGB\n"
	got := bundledScreener(t).Screen(context.Background(), text)
	if len(got.Spans) == 0 {
		t.Fatal("expected the bundled catalog to name this value")
	}
	span := got.Spans[0]
	if span.State != StateDetected {
		t.Fatalf("state = %q, want detected", span.State)
	}
	if span.Reference != "" {
		t.Fatal("an untracked detection has no capability reference")
	}
	if span.RuleID == "" || span.RuleTitle == "" {
		t.Fatal("a span must name the evidence that produced it")
	}
	captured := string([]rune(text)[span.Start:span.End])
	if !strings.Contains(captured, "AKIAQYJK5TXV4NZR7SGB") {
		t.Fatalf("span %d..%d does not cover the value", span.Start, span.End)
	}
}

func TestScreenTruncatesAboveTheCapAndSaysSo(t *testing.T) {
	text := strings.Repeat("x", ScanByteCap+64)
	got := bundledScreener(t).Screen(context.Background(), text)
	if !got.Truncated {
		t.Fatal("a document past the cap must report truncation")
	}
	if got.ScreenedBytes > ScanByteCap {
		t.Fatalf("screened %d bytes, above the cap", got.ScreenedBytes)
	}
}

func TestScreenWithoutAMatcherIsUnavailable(t *testing.T) {
	got := New(nil).Screen(context.Background(), "AKIAQYJK5TXV4NZR7SGB")
	if got != nil {
		t.Fatalf("screen = %+v, want unavailable", got)
	}
}

func TestScreenNamesItsCatalog(t *testing.T) {
	got := bundledScreener(t).Screen(context.Background(), "const answer = 42\n")
	if got.CatalogVersion == "" {
		t.Fatal("a completed screen must name its catalog")
	}
}

func TestStateSeparatesLiveCapabilitiesFromRetiredEvidence(t *testing.T) {
	live := secretmatch.Match{RuleID: secretmatch.ManagedRuleID, Reference: "{{paintedwolf-secret:x}}"}
	// Another chat's capability: live, but this audience holds no reference.
	unreferenced := secretmatch.Match{RuleID: secretmatch.ManagedRuleID}
	retired := secretmatch.Match{RuleID: secretmatch.ManagedRuleID, Retired: true}
	shape := secretmatch.Match{RuleID: "aws-access-key"}
	if got := stateOf(live); got != StateTracked {
		t.Fatalf("live capability = %q, want tracked", got)
	}
	if got := stateOf(unreferenced); got != StateTracked {
		t.Fatalf("unreferenced live capability = %q, want tracked", got)
	}
	if got := stateOf(retired); got != StateRetired {
		t.Fatalf("retired capability = %q, want retired", got)
	}
	if got := stateOf(shape); got != StateDetected {
		t.Fatalf("shape hit = %q, want detected", got)
	}
}
