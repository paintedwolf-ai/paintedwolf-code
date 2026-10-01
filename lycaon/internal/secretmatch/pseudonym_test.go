package secretmatch

import (
	"context"
	"strings"
	"testing"
)

// A capture is only useful if you can tell whether two markers replaced the
// same value. The tag supplies that identity; the value stays absent.
func TestPseudonymsIdentifyAValueWithoutDisclosingIt(t *testing.T) {
	m := fingerprintedHarvest(t,
		HarvestedValue{Name: "A", Container: ".env", Secret: "alpha-value-one-long"},
		HarvestedValue{Name: "B", Container: ".env", Secret: "beta-value-two-long"},
	)
	in := "first alpha-value-one-long then beta-value-two-long then alpha-value-one-long again"
	redacted, spans := redactMatches(in, m.screenRaw(context.Background(), in))
	tagged := ApplyPseudonyms(redacted, spans)

	for _, secret := range []string{"alpha-value-one-long", "beta-value-two-long"} {
		if strings.Contains(tagged, secret) {
			t.Fatalf("pseudonymised output still holds %q: %s", secret, tagged)
		}
	}
	markers := markerTags(tagged)
	if len(markers) != 3 {
		t.Fatalf("markers = %d, want 3: %s", len(markers), tagged)
	}
	if markers[0] != markers[2] {
		t.Errorf("the same value got different tags (%q, %q) — occurrences cannot be correlated",
			markers[0], markers[2])
	}
	if markers[0] == markers[1] {
		t.Errorf("two different values share tag %q — correlation would be wrong", markers[0])
	}
}

// A span with no fingerprint keeps the plain marker rather than emitting a
// half-formed tag.
func TestPseudonymsLeavePlainMarkersWhenNoIdentityExists(t *testing.T) {
	out := ApplyPseudonyms("a [REDACTED] b", []RedactionSpan{{Start: 2, Length: 10}})
	if out != "a [REDACTED] b" {
		t.Errorf("output = %q, want the plain marker preserved", out)
	}
}

// fingerprintedHarvest mirrors the app wiring: the runtime fingerprints every
// harvested value, and the tag is derived from that identity.
func fingerprintedHarvest(t *testing.T, values ...HarvestedValue) *Matcher {
	t.Helper()
	m := loadBundled(t)
	fp, err := NewFingerprinter([]byte(strings.Repeat("p", fingerprintKeyBytes)))
	if err != nil {
		t.Fatalf("NewFingerprinter: %v", err)
	}
	m.SetFingerprinter(fp)
	for i := range values {
		values[i] = asContainerValue(values[i])
		values[i].Fingerprint = fp.Fingerprint(values[i].Secret)
	}
	m.SetHarvestSource(func(context.Context) []HarvestedValue { return values })
	return m
}

func markerTags(s string) []string {
	var out []string
	for rest := s; ; {
		i := strings.Index(rest, "[REDACTED:")
		if i < 0 {
			return out
		}
		rest = rest[i+len("[REDACTED:"):]
		end := strings.Index(rest, "]")
		if end < 0 {
			return out
		}
		out = append(out, rest[:end])
		rest = rest[end+1:]
	}
}
