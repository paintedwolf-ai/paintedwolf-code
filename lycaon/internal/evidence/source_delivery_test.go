package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestDeliveredSourceQualifiesOnlyActualExactMappedPage(t *testing.T) {
	captured := []byte("unseen first\nshown second\nunseen third\n")
	sum := sha256.Sum256(captured)
	pin := toolcontract.SourcePin{SourceID: "source:fixture", Revision: "revision:one", Representation: toolcontract.UTF8SourceBytes, SHA256: hex.EncodeToString(sum[:]), EndByte: int64(len(captured))}
	page := []byte("shown second\n")
	mapping := toolcontract.StreamSourceMapping{Source: pin, SourceStartByte: 13, SourceEndByte: 26, OutputEndByte: 13}
	observation, err := ConfirmDeliveredSource("src/fixture.txt", pin, captured, page, mapping, "response:actual", "delivery:actual")
	if err != nil {
		t.Fatalf("confirm actual delivered page: %v", err)
	}
	record := observation.Record()
	record.Handle = "read#1"
	ledger := AssembleLedger([]Record{record})
	resolution := ResolveObservation(CitationRoots{}, ledger, "read#1", Triple{Path: "src/fixture.txt", Line: 3, Excerpt: "unseen third"})
	if resolution.Verdict != VerdictUnverifiable {
		t.Fatalf("unseen mapped range received fallback trace verdict: %+v", resolution)
	}
	for _, tc := range []struct {
		line    int
		excerpt string
		want    bool
	}{{1, "unseen first", false}, {2, "shown second", true}, {3, "unseen third", false}, {1, "", false}, {3, "", false}, {0, "unseen first", false}, {0, "shown second", true}} {
		got, _ := VerifyFileRegion(record, Claim{Path: "src/fixture.txt", Line: tc.line, Excerpt: tc.excerpt})
		if got != tc.want {
			t.Fatalf("line%d excerpt%q qualified=%t want%t", tc.line, tc.excerpt, got, tc.want)
		}
	}
	// Merely retaining captured bytes constructs no record. A transformation
	// cannot borrow the source mapping even if it prints a source-like path.
	for _, bytes := range [][]byte{[]byte("SHOWN SECOND\n"), []byte("other second\n")} {
		if _, err = ConfirmDeliveredSource("src/fixture.txt", pin, captured, bytes, mapping, "response:actual", "delivery:actual"); err == nil {
			t.Fatal("transformed bytes qualified")
		}
	}
	if _, err = ConfirmDeliveredSource("src/fixture.txt", pin, captured, page, mapping, "", ""); err == nil {
		t.Fatal("retained but undelivered page qualified")
	}
	clone := observation.Record()
	clone.SourceDelivery.StartLine = 1
	clone.Body[0] = "unseen first"
	original := observation.Record()
	if original.SourceDelivery.StartLine != 2 || original.Body[0] != "shown second" {
		t.Fatal("observation shared mutable record data")
	}
}
