package visual

import (
	"testing"
)

func TestDurableArtifactRecordsFixture(t *testing.T) {
	rows := LoadDurableArtifactRecords(t)
	if len(rows) < 3 {
		t.Fatalf("rows = %d want >= 3 (render, capture, dedup re-present)", len(rows))
	}
	for _, row := range rows {
		t.Run(row.ID, func(t *testing.T) {
			AssertArtifactRecord(t, row)
		})
	}

	var (
		renderHash string
		renderID   string
		sawCapture bool
		sawDedup   bool
	)
	for _, row := range rows {
		switch {
		case row.Source == "render" && row.EvidenceHandle == "" && renderHash == "":
			renderHash = row.ContentHash
			renderID = row.ID
		case row.Source == "capture" && row.EvidenceHandle != "":
			sawCapture = true
		case renderHash != "" && row.ContentHash == renderHash && row.ID != renderID:
			sawDedup = true
		}
	}
	if renderHash == "" {
		t.Fatal("want a render row with empty evidence_handle")
	}
	if !sawCapture {
		t.Fatal("want a capture row with non-empty evidence_handle")
	}
	if !sawDedup {
		t.Fatal("want two ids sharing one hash (content-addressed dedup case)")
	}
}

func TestAssertNoPathLeak(t *testing.T) {
	// Direct unit of the no-homedir-leak gate used by AssertArtifactRecord.
	assertNoPathLeak(t, "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08")
}
