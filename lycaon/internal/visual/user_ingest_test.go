package visual

import (
	"bytes"
	"image"
	"image/png"
	"testing"

	"github.com/google/uuid"
)

func TestIngestUserImage_pngRoundTrip(t *testing.T) {
	store := NewMemoryStore()
	raw := tinyPNG(t)
	wire, err := IngestUserImage(t.Context(), store, "sess-1", uuid.NewString(), "image/png", raw)
	if err != nil {
		t.Fatalf("IngestUserImage: %v", err)
	}
	if wire.Source != "user" {
		t.Fatalf("source = %q want user", wire.Source)
	}
	if !wire.Perceive || !wire.StoreRef || wire.ID == "" {
		t.Fatalf("wire = %+v", wire)
	}
	res := store.Resolve(t.Context(), "sess-1", wire.ID)
	if !res.IsPresent() || len(res.Bytes()) == 0 {
		t.Fatalf("resolve after ingest: present=%v", res.IsPresent())
	}
}

func TestRejectUnsafeUserImage_svg(t *testing.T) {
	err := RejectUnsafeUserImage("image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`))
	if err == nil {
		t.Fatal("expected svg reject")
	}
	err = RejectUnsafeUserImage("image/png", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`))
	if err == nil {
		t.Fatal("expected markup sniff reject")
	}
}

func TestRejectUnsafeUserImage_urlNotAcceptedAsMime(t *testing.T) {
	err := RejectUnsafeUserImage("text/uri-list", []byte("https://evil.example/x.png"))
	if err == nil {
		t.Fatal("expected reject")
	}
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return buf.Bytes()
}
