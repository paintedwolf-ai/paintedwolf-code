package visual

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
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
	assertNoPathLeak(t, "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08")
}

func TestVisualSystemicInvariants(t *testing.T) {
	t.Run("content-addressed deduplication preserves shared blob until last survivor is deleted", func(t *testing.T) {
		f := newDurableFixture(t, []string{"root-1"})
		shared := append(onePixelPNG(t), "shared-payload"...)
		first, err := f.store.Put(t.Context(), "root-1", Entry{
			Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender, Caption: "first"},
			Bytes: shared,
		})
		testutil.FailErr(t, "Put first", err)
		second, err := f.store.Put(t.Context(), "root-1", Entry{
			Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender, Caption: "second"},
			Bytes: shared,
		})
		testutil.FailErr(t, "Put second", err)

		_, found, err := f.store.Delete(t.Context(), f.project, first.ID, "human_delete")
		testutil.FailErr(t, "Delete first", err)
		if !found {
			t.Fatal("expected first artifact to exist")
		}
		recSecond := f.record(t, second.ID)
		if !f.blobExists(t, recSecond) {
			t.Fatal("shared blob was removed while second record was still live")
		}
		if res := f.store.Resolve(t.Context(), "root-1", second.ID); !res.IsPresent() {
			t.Fatalf("second artifact failed to resolve: %v", res.Reason())
		}

		_, found, err = f.store.Delete(t.Context(), f.project, second.ID, "human_delete")
		testutil.FailErr(t, "Delete second", err)
		if !found {
			t.Fatal("expected second artifact to exist")
		}
		if f.blobExists(t, recSecond) {
			t.Fatal("shared blob survived after last referencing record was deleted")
		}
	})

	t.Run("embedded ArtifactRecord sub-structs preserve flat JSON wire serialization", func(t *testing.T) {
		f := newDurableFixture(t, []string{"root-1"})
		rec := recordFor(f, "art-json", []byte("payload"))
		rec.Caption = "Flat JSON check"
		rec.RetentionClass = "working"
		raw, err := json.Marshal(rec)
		testutil.FailErr(t, "marshal ArtifactRecord", err)

		var flat map[string]any
		testutil.FailErr(t, "unmarshal map", json.Unmarshal(raw, &flat))
		for _, key := range []string{"id", "project_id", "content_hash", "byte_size", "caption", "retention_class"} {
			if _, ok := flat[key]; !ok {
				t.Fatalf("expected top-level JSON key %q in %s", key, string(raw))
			}
		}
		var roundTrip ArtifactRecord
		testutil.FailErr(t, "unmarshal ArtifactRecord", json.Unmarshal(raw, &roundTrip))
		if roundTrip != rec {
			t.Fatalf("round-trip mismatch: got %+v want %+v", roundTrip, rec)
		}
	})

	t.Run("nil receiver entrypoints degrade safely without panicking", func(t *testing.T) {
		var nilStore *DurableStore
		if _, err := nilStore.Put(t.Context(), "root-1", Entry{}); err == nil {
			t.Fatal("nilStore.Put succeeded, want error")
		}
		if res := nilStore.Resolve(t.Context(), "root-1", "id"); res.Reason() != AbsenceUnknown {
			t.Fatalf("nilStore.Resolve reason = %q want %q", res.Reason(), AbsenceUnknown)
		}
		var nilRecords *Records
		if _, found, err := nilRecords.Get(t.Context(), "id"); err != nil || found {
			t.Fatalf("nilRecords.Get = found:%v err:%v", found, err)
		}
		if _, err := nilRecords.ListTree(t.Context(), "proj-1", "root-1"); err != nil {
			t.Fatalf("nilRecords.ListTree err = %v", err)
		}
	})
}
