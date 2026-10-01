package visual

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestResolveInTree(t *testing.T) {
	store := NewMemoryStore()
	art, err := store.Put(t.Context(), "root-a", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture},
		Bytes: TestPNG1x1Bytes(),
	})
	testutil.FailErr(t, "put root-a artifact", err)
	if res := ResolveInTree(t.Context(), store, "root-a", art.ID); !res.IsPresent() {
		t.Fatalf("same tree: %s", res.Note())
	}
	if res := ResolveInTree(t.Context(), store, "root-a", "missing-id"); res.Reason() != AbsenceUnknown {
		t.Fatalf("missing = %q want unknown", res.Reason())
	}
	_, err = store.Put(t.Context(), "root-b", Entry{
		Meta:  api.VisualArtifact{ID: art.ID, Mime: "image/png", Source: api.VisualArtifactSourceCapture},
		Bytes: TestPNG1x1Bytes(),
	})
	testutil.FailErr(t, "put colliding root-b artifact", err)
	// Resolve within the requested tree when ids collide.
	if res := ResolveInTree(t.Context(), store, "root-a", art.ID); !res.IsPresent() {
		t.Fatalf("root-a still resolves id: %s", res.Note())
	}
	foreignID := "only-in-b"
	_, err = store.Put(t.Context(), "root-b", Entry{
		Meta:  api.VisualArtifact{ID: foreignID, Mime: "image/png", Source: api.VisualArtifactSourceRender},
		Bytes: TestPNG1x1Bytes(),
	})
	testutil.FailErr(t, "put root-b artifact", err)
	if res := ResolveInTree(t.Context(), store, "root-a", foreignID); res.Reason() != AbsenceForeign {
		t.Fatalf("foreign = %q want foreign", res.Reason())
	}
}
