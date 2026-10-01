package evidence_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
)

// TestBuildRecord_scanPackGroundsFindings covers scan_pack artifact grounding.
func TestBuildRecord_scanPackGroundsFindings(t *testing.T) {
	dir := t.TempDir()
	body := `{"scan_ids":["engine-1"],"per_scan":[{"scan_id":"engine-1","guidance_preview":[` +
		`{"message":"Dynamic execution using exec or eval","file":"shellsim/builtins.py","line":144,"rule_id":"r1"}` +
		`]}],"findings_count":1,"status":"complete"}`

	rec := evidence.BuildEvidenceRecord(dir, "scan_pack", map[string]any{}, body)
	if rec.Kind != "scan" || rec.Shape != evidence.ShapeArtifact {
		t.Fatalf("kind=%q shape=%q want scan/artifact", rec.Kind, rec.Shape)
	}
	if rec.Fidelity != evidence.FidelityStructured {
		t.Fatalf("trust = %q want structured", rec.Fidelity)
	}

	if ok, _ := evidence.VerifyRecord(rec, evidence.Claim{Excerpt: "Dynamic execution using exec or eval"}); !ok {
		t.Fatal("a cited finding message must ground against the captured scan body")
	}
	if ok, _ := evidence.VerifyRecord(rec, evidence.Claim{Path: "shellsim/builtins.py"}); !ok {
		t.Fatal("a cited flagged file must ground against the scan finding paths")
	}
}
