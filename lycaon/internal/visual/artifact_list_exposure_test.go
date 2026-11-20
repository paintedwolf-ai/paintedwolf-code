package visual

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/exposure"
	"github.com/lycaon/lycaon/pkg/api"
)

// The artifact list names stored artifacts by reference: neither the bytes
// nor the host directory that holds them reach the response.
func TestArtifactListCarriesReferencesNotBytesOrPaths(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	raw := onePixelPNG(t)
	entry := Entry{Meta: api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender, Caption: "mockup"}, Bytes: raw}
	durable, err := f.store.Put(t.Context(), "root-1", entry)
	testutil.FailErr(t, "durable Put failed", err)
	memory := NewMemoryStore()
	hot, err := memory.Put(t.Context(), "root-1", entry)
	testutil.FailErr(t, "memory Put failed", err)

	for _, tc := range []struct {
		name  string
		id    string
		store Store
	}{
		{name: "durable", id: durable.ID, store: f.store},
		{name: "memory", id: hot.ID, store: memory},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items, err := tc.store.ListTree(t.Context(), "root-1")
			testutil.FailErr(t, "ListTree failed", err)
			body, err := json.Marshal(api.ArtifactListResponse{Artifacts: items})
			testutil.FailErr(t, "marshal list", err)
			if len(items) != 1 {
				t.Fatalf("listed %d artifacts, want 1", len(items))
			}
			exposure.Present(t, string(body), tc.id)
			exposure.Absent(t, string(body), base64.StdEncoding.EncodeToString(raw), f.dataDir, f.blobDir(t))
		})
	}
}
