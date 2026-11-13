package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func deliveredSpanFixture(t *testing.T, captured, delivered string, start int, revision string) Record {
	t.Helper()
	digest := sha256.Sum256([]byte(captured))
	pin := toolcontract.SourcePin{SourceID: "source:fixture", Revision: revision, Representation: toolcontract.UTF8SourceBytes, SHA256: hex.EncodeToString(digest[:]), EndByte: int64(len(captured))}
	mapping := toolcontract.StreamSourceMapping{Source: pin, SourceStartByte: int64(start), SourceEndByte: int64(start + len(delivered)), OutputEndByte: int64(len(delivered))}
	observation, err := ConfirmDeliveredSource("fixture.py", pin, []byte(captured), []byte(delivered), mapping, "response", "delivery")
	if err != nil {
		testutil.FailErr(t, "confirm source span", err)
	}
	return observation.Record()
}

func TestDeliveredSourceContiguousExcerpt(t *testing.T) {
	const page = "def test_catalog():\n    result = catalog()\n\n    assert result == expected\n"
	rec := deliveredSpanFixture(t, "hidden header\n"+page+"hidden footer\n", page, len("hidden header\n"), "one")
	for _, tc := range []struct {
		name   string
		claim  Claim
		reason string
	}{
		{"multiline", Claim{Line: 2, Excerpt: "def test_catalog():\n    result = catalog()"}, ""},
		{"without coordinate", Claim{Excerpt: "result = catalog()\n\n    assert result == expected"}, ""},
		{"blank line preserved", Claim{Line: 3, Excerpt: "result = catalog()\n\n    assert result == expected"}, ""},
		{"blank line omitted", Claim{Line: 3, Excerpt: "result = catalog()\n    assert result == expected"}, VerifyReasonSourceExcerpt},
		{"wrong first line", Claim{Line: 2, Excerpt: "result = catalog()\n\n    assert result == expected"}, VerifyReasonSourceExcerpt},
		{"outside start", Claim{Line: 1, Excerpt: "hidden header"}, VerifyReasonSourceRange},
		{"outside end", Claim{Line: 5, Excerpt: "assert result == expected\nhidden footer"}, VerifyReasonSourceRange},
		{"wrong path", Claim{Path: "another.py", Line: 2, Excerpt: "def test_catalog():"}, VerifyReasonSourcePath},
		{"single line", Claim{Line: 5, Excerpt: "assert result == expected"}, ""},
		{"empty excerpt", Claim{Line: 5}, ""},
		{"no coordinates", Claim{}, ""},
		{"too short", Claim{Line: 2, Excerpt: "def"}, VerifyReasonSourceExcerpt},
		{"unseen body", Claim{Excerpt: "hidden footer"}, VerifyReasonSourceExcerpt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := VerifyFileRegion(rec, tc.claim)
			if ok != (tc.reason == "") || reason != tc.reason {
				t.Fatalf("verification = %t, %q; want reason %q", ok, reason, tc.reason)
			}
		})
	}
	rec.Body = rec.Body[:1]
	if ok, reason := VerifyFileRegion(rec, Claim{}); ok || reason != VerifyReasonSourceWindow {
		t.Fatalf("invalid window accepted: %t, %q", ok, reason)
	}
}

func TestDeliveredSourceClippingDoesNotBorrowCapturedBytes(t *testing.T) {
	const captured = "unseen prefix shown suffix\nsecond complete line\nlast clipped tail\n"
	const page = "shown suffix\nsecond complete line\nlast clipped"
	rec := deliveredSpanFixture(t, captured, page, strings.Index(captured, "shown"), "one")
	for _, tc := range []struct {
		excerpt string
		want    bool
	}{
		{"shown suffix\nsecond complete line\nlast clipped", true},
		{"unseen prefix shown suffix\nsecond complete line", false},
		{"second complete line\nlast clipped tail", false},
	} {
		ok, _ := VerifyFileRegion(rec, Claim{Excerpt: tc.excerpt})
		if ok != tc.want {
			t.Fatalf("clipped excerpt %q accepted=%t", tc.excerpt, ok)
		}
	}
}

func TestDeliveredSourceResolutionKeepsObservationRevision(t *testing.T) {
	old := deliveredSpanFixture(t, "first old line\nsecond old line\n", "first old line\n", 0, "old")
	old.Handle = "read#1"
	newer := deliveredSpanFixture(t, "first new line\nsecond new line\n", "second new line\n", len("first new line\n"), "new")
	newer.Handle = "read#2"
	ev := AssembleLedger([]Record{old, newer})
	for _, excerpt := range []string{"first old line\nsecond new line", "first old line\nsecond old line", "second new line"} {
		res := ResolveObservation(CitationRoots{}, ev, old.Handle, Triple{Path: "fixture.py", Line: 1, Excerpt: excerpt})
		if res.Verdict != VerdictUnverifiable {
			t.Fatalf("borrowed source bytes for %q: %+v", excerpt, res)
		}
	}
}

func TestDeliveredSourceReanchorUsesExactSpan(t *testing.T) {
	const body = "first source line\nsecond source line\nthird source line\n"
	rec := deliveredSpanFixture(t, body, body, 0, "one")
	rec.Handle = "read#1"
	ev := AssembleLedger([]Record{rec})
	line, handle, ok := reanchorExcerpt(ev, "fixture.py", "second source line\nthird source line")
	if !ok || line != 2 || handle != rec.Handle {
		t.Fatalf("reanchor = %d, %q, %t", line, handle, ok)
	}
	if _, _, ok := reanchorExcerpt(ev, "fixture.py", "second  source line\nthird source line"); ok {
		t.Fatal("reanchor accepted transformed source bytes")
	}
}
