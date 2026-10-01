package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// A reader who was not in the chat can find the app from the document itself:
// the credit names the site and the site is a live link.
func TestColophon_CreditLinksProductSite(t *testing.T) {
	input := loadReportInputFixture(t, "synthesis_only.json")

	joined := joinRowValues(colophonBlocks(testMeasurer(t), input))
	if !strings.Contains(joined, productSiteLabel) {
		t.Fatalf("colophon = %q; want the credit to name %q", joined, productSiteLabel)
	}

	pdf, err := Render(input)
	testutil.FailErr(t, "render", err)
	if !bytes.Contains(pdf, []byte("/URI ("+productSite+")")) {
		t.Fatalf("rendered report carries no link annotation to %s", productSite)
	}
}
