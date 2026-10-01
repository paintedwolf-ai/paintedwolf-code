package visual

import (
	"bytes"
	"image"
	"image/png"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func encodePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	testutil.FailErr(t, "encode png", png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, height))))
	return buf.Bytes()
}

func TestPutStampsRasterDimensionsFromBytes(t *testing.T) {
	store := NewMemoryStore()
	wire, err := store.Put(t.Context(), "root", Entry{
		// Declared dimensions are not trusted; the header is.
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture, Width: 999, Height: 999},
		Bytes: encodePNG(t, 1280, 800),
	})
	testutil.FailErr(t, "put png", err)
	if wire.Width != 1280 || wire.Height != 800 {
		t.Fatalf("wire size = %dx%d want 1280x800", wire.Width, wire.Height)
	}
	res := store.Resolve(t.Context(), "root", wire.ID)
	if !res.IsPresent() || res.Meta().Width != 1280 || res.Meta().Height != 800 {
		t.Fatalf("resolved meta = %+v", res.Meta())
	}
}

func TestPutLeavesTimeBasedMediaUnsized(t *testing.T) {
	store := NewMemoryStore()
	wire, err := store.Put(t.Context(), "root", Entry{
		Meta:  LiveToolRecordingMeta("video/mp4", "page-1", "", "call-1", time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC), 1500),
		Bytes: []byte("mp4 container bytes"),
	})
	testutil.FailErr(t, "put video", err)
	if wire.Width != 0 || wire.Height != 0 {
		t.Fatalf("video carried dimensions %dx%d", wire.Width, wire.Height)
	}
}

func TestPutRefusesUndecodableRaster(t *testing.T) {
	store := NewMemoryStore()
	_, err := store.Put(t.Context(), "root", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender},
		Bytes: []byte("not a png"),
	})
	if err == nil {
		t.Fatal("undecodable raster was stored")
	}
	items, listErr := store.ListTree(t.Context(), "root")
	testutil.FailErr(t, "list tree", listErr)
	if len(items) != 0 {
		t.Fatalf("refused raster left %d artifacts", len(items))
	}
}

func TestDurableStoreRecordCarriesRasterDimensionsAcrossRestart(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	wire, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender},
		Bytes: encodePNG(t, 640, 360),
	})
	testutil.FailErr(t, "put png", err)
	if rec := f.record(t, wire.ID); rec.Width != 640 || rec.Height != 360 {
		t.Fatalf("record size = %dx%d want 640x360", rec.Width, rec.Height)
	}
	// A restarted store has an empty hot tier, so metadata comes from the row.
	res := f.restart().Resolve(t.Context(), "root-1", wire.ID)
	if !res.IsPresent() {
		t.Fatalf("resolve after restart: %s", res.Note())
	}
	if res.Meta().Width != 640 || res.Meta().Height != 360 {
		t.Fatalf("resolved size = %dx%d want 640x360", res.Meta().Width, res.Meta().Height)
	}
}
