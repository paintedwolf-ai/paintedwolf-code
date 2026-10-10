package visual

import (
	"context"
	"github.com/lycaon/lycaon/internal/testutil"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

type bindingContextKey struct{}

func TestCommittedEvidenceBindingPreservesContext(t *testing.T) {
	hot := NewMemoryStore()
	artifact, err := hot.Put(t.Context(), "tree", Entry{Meta: api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture}, Bytes: TestPNG1x1Bytes()})
	if err != nil {
		testutil.FailErr(t, "store visual artifact", err)
	}
	records := &Records{}
	NewDurableStore(DurableConfig{DataDir: t.TempDir(), Hot: hot, Records: records})
	bind := records.onHandleBound
	var observed any
	records.onHandleBound = func(ctx context.Context, artifactID, handle string) {
		observed = ctx.Value(bindingContextKey{})
		bind(ctx, artifactID, handle)
	}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), bindingContextKey{}, "committed-binding"))
	cancel()
	records.EvidenceHandleBound(ctx, artifact.ID, "evidence")
	if observed != "committed-binding" {
		t.Fatalf("committed binding context=%v", observed)
	}
	got := hot.Resolve(t.Context(), "tree", artifact.ID)
	if !got.IsPresent() || got.Meta().EvidenceHandle != "evidence" {
		t.Fatalf("committed hot binding=%+v", got.Meta())
	}
}
