package evidence_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
)

// Command, opaque and structured-event records verify by verbatim substring of
// the captured body, whatever their kind.
func TestVerbatimShapesAcceptCapturedTextAndRejectAlteredText(t *testing.T) {
	body := []string{"exit status 0: 14 packages compiled cleanly"}
	for _, shape := range []string{evidence.ShapeCommand, evidence.ShapeOpaque, evidence.ShapeStructuredEvent} {
		t.Run(shape, func(t *testing.T) {
			ev := evidence.Ledger{Handles: map[string]evidence.Record{
				"probe#1": {Handle: "probe#1", Kind: "probe", Shape: shape, Body: body},
			}}
			if ok, reason := evidence.VerifyHandle(ev, "probe#1", evidence.Claim{Excerpt: "14 packages compiled cleanly"}); !ok {
				t.Fatalf("verbatim excerpt rejected: %q", reason)
			}
			if ok, _ := evidence.VerifyHandle(ev, "probe#1", evidence.Claim{Excerpt: "15 packages compiled cleanly"}); ok {
				t.Fatal("altered excerpt accepted")
			}
		})
	}
}
