package visual

import (
	"testing"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestArtifactDeletionRetainsCapturedBodyUntilArchiveCompletes(t *testing.T) {
	f := newDurableFixture(t, []string{"session"})
	artifact, err := f.store.Put(t.Context(), "session", Entry{
		Meta: api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender}, Bytes: onePixelPNG(t),
	})
	testutil.FailErr(t, "publish captured artifact", err)
	record := f.record(t, artifact.ID)
	release := bloblifecycle.AcquirePublication(f.dataDir)
	_, found, err := f.store.Delete(t.Context(), f.project, artifact.ID, "human_delete")
	if err != nil || !found {
		release()
		t.Fatalf("delete captured artifact: found=%v err=%v", found, err)
	}
	retained := f.blobExists(t, record)
	release()
	if !retained {
		t.Fatal("artifact bytes disappeared during archive capture")
	}
	testutil.FailErr(t, "reconcile after archive", f.store.reconcileProjectStorage(t.Context(), f.project, f.blobDir(t)))
	if f.blobExists(t, record) {
		t.Fatal("unreferenced artifact bytes survived completed archive capture")
	}
}
