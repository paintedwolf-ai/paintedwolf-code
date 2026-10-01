package visual

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestResolveRef_uuidAndHandle(t *testing.T) {
	store := NewMemoryStore()
	wire, err := store.Put(t.Context(), "root", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender, EvidenceHandle: "render#1"},
		Bytes: TestPNG1x1Bytes(),
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	id, res := ResolveRef(t.Context(), store, "root", wire.ID)
	if !res.IsPresent() || id != wire.ID {
		t.Fatalf("UUID resolve: id=%q present=%v", id, res.IsPresent())
	}
	id, res = ResolveRef(t.Context(), store, "root", "render#1")
	if !res.IsPresent() || id != wire.ID {
		t.Fatalf("handle resolve: id=%q present=%v want %q", id, res.IsPresent(), wire.ID)
	}
	// Misses return no artifact identity.
	id, res = ResolveRef(t.Context(), store, "root", "render#99")
	if res.IsPresent() || id != "" || res.Reason() != AbsenceUnknown {
		t.Fatalf("missing handle: id=%q present=%v reason=%q", id, res.IsPresent(), res.Reason())
	}
}
