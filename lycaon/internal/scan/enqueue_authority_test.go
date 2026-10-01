package scan

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEnqueueUsesPublishedSnapshotProvenanceWithoutRegistry(t *testing.T) {
	store := authorityTestStore(t)
	coordinator := newTestCoordinator(t, store, nil)
	root := t.TempDir()
	id := publishTestSnapshot(t, coordinator.snapshots, root)
	snapshot, err := coordinator.snapshots.Get(t.Context(), id)
	testutil.FailErr(t, "read published snapshot", err)
	scan, err := coordinator.Enqueue(t.Context(), EnqueueRequest{
		ProjectDir: root, Categories: []api.ScanCategory{api.ScanCategorySAST},
		ScannerID: "lycaon-sast", SourceSnapshotID: id,
	})
	testutil.FailErr(t, "enqueue published snapshot", err)
	if scan.SourceCaptureQuality != string(snapshot.Quality) || scan.SourceAdmissionMode != string(snapshot.AdmissionMode) {
		t.Fatalf("scan provenance = %q/%q, want %q/%q", scan.SourceCaptureQuality, scan.SourceAdmissionMode, snapshot.Quality, snapshot.AdmissionMode)
	}
}

func TestEnqueueRefusesMissingSnapshotAndUnadmittedTargetsWithoutRegistry(t *testing.T) {
	for _, published := range []bool{false, true} {
		name := "missing snapshot"
		if published {
			name = "unadmitted target"
		}
		t.Run(name, func(t *testing.T) {
			store := authorityTestStore(t)
			coordinator := newTestCoordinator(t, store, nil)
			root := t.TempDir()
			id := "missing-snapshot"
			if published {
				id = publishTestSnapshot(t, coordinator.snapshots, root)
			}
			got, err := coordinator.Enqueue(t.Context(), EnqueueRequest{
				ProjectDir: root, Categories: []api.ScanCategory{api.ScanCategorySAST},
				ScannerID: "lycaon-sast", SourceSnapshotID: id, Paths: []string{"missing.go"},
			})
			if got != nil || err == nil {
				t.Fatalf("enqueue invalid source = %+v, %v; want refusal", got, err)
			}
		})
	}
}
