package evidence

import "testing"

func TestLaterFileOfNumberedReadSupportsNoCountedCoordinate(t *testing.T) {
	body := []string{"one", "two", "three"}
	rec := Record{Path: "b.go", Body: body, LineRanges: []LineRange{{Start: 1, End: 3}},
		SourceDelivery: &SourceDelivery{StartLine: 1, EndLine: 3}}
	if got := CoordinateSupport(rec, "b.go", 2, 2); got != CoordinateDelivered {
		t.Fatalf("unnumbered delivery = %v, want delivered", got)
	}
	rec.SourceDelivery.PrintedOtherNumbers = true
	if got := CoordinateSupport(rec, "b.go", 2, 2); got != CoordinateUnsupported {
		t.Fatalf("printed other numbers = %v, want unsupported", got)
	}
	if !ExcerptVerifiesRange(rec, "b.go", "two", 2, 2) {
		t.Fatal("an excerpt no longer verifies the line")
	}
}
