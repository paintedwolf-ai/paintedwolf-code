package curl

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// TestCatalogGoldenCurlRefusals runs each single-statement curl golden
// program that the owner refuses before anything is read or sent.
func TestCatalogGoldenCurlRefusals(t *testing.T) {
	catalog, err := toolcontract.LanguageV1()
	testutil.FailErr(t, "catalog", err)
	var contract struct {
		Golden []struct {
			ID       string `json:"id"`
			Program  string `json:"program"`
			Expected struct {
				Diagnostic *string `json:"diagnostic"`
			} `json:"expected"`
		} `json:"golden_programs"`
	}
	testutil.FailErr(t, "golden contract", json.Unmarshal(catalog.Contracts["golden"], &contract))
	h := newHarness(t)
	ran := 0
	for _, golden := range contract.Golden {
		words := strings.Fields(golden.Program)
		if len(words) == 0 || words[0] != "curl" || golden.Expected.Diagnostic == nil {
			continue
		}
		t.Run(golden.ID, func(t *testing.T) {
			r := h.curl(t.Context(), words[1:]...)
			if reject := toolrejection.AsToolReject(r.err); reject == nil || reject.Code != *golden.Expected.Diagnostic || r.transfer != (Transfer{}) {
				t.Fatalf("%s: %v, want %s", golden.Program, r.err, *golden.Expected.Diagnostic)
			}
		})
		ran++
	}
	if ran < 5 {
		t.Fatalf("ran %d curl golden refusals", ran)
	}
}
