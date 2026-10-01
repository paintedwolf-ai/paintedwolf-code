package webresearch

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/visualscreen"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFetchURLImageWithoutDest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	allowLoopbackFetch(t)

	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="100" height="100"><text>Architecture Map</text></svg>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write([]byte(svg))
	}))
	defer srv.Close()

	tempDir := t.TempDir()
	reg := tools.NewDefaultRegistry()
	boundary := rawTestBoundary(t)
	deps := ToolDeps{
		Boundary:     boundary,
		VisualScreen: visualscreen.NewGate(visualscreen.NewScanner(nil), nil, nil),
	}
	testutil.FailErr(t, "RegisterToolsWithFactory", RegisterToolsWithFactory(reg, deps, nil))

	tctx := tools.ToolContext{
		Roots: []projectroot.RootRef{
			{ID: "r1", Path: tempDir, IsPrimary: true},
		},
		ActiveRootID: "r1",
		SessionID:    "s1",
		ToolCallID:   "call1",
		Out:          &tools.ToolInvocationOut{},
	}

	out, err := reg.Run(context.Background(), "fetch_url", map[string]any{
		"url": srv.URL,
	}, tctx)
	testutil.FailErr(t, "fetch_url", err)

	// A fetched image becomes a visual capture, never a workspace file.
	entries, err := os.ReadDir(tempDir)
	testutil.FailErr(t, "read tempDir", err)
	if len(entries) != 0 {
		t.Fatalf("expected 0 files in workspace, got %d", len(entries))
	}

	if tctx.Out.Visual == nil {
		t.Fatal("expected tctx.Out.Visual to be populated")
	}
	if !tctx.Out.Visual.Perceive {
		t.Error("expected Perceive = true")
	}
	if tctx.Out.Visual.Source != api.VisualArtifactSourceFetch {
		t.Errorf("fetched image source = %q, want fetch", tctx.Out.Visual.Source)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("failed to parse receipt JSON: %v, raw: %s", err, out)
	}
	if parsed["mode"] != "visual" {
		t.Errorf("expected mode=visual, got: %v", parsed["mode"])
	}
	if _, ok := parsed["handle"]; ok {
		t.Errorf("receipt carries a tool-minted handle: %v", parsed["handle"])
	}
	if text, ok := parsed["text"].(string); !ok || !strings.Contains(text, "Architecture Map") {
		t.Errorf("expected OCR/extracted text to contain Architecture Map, got: %v", parsed["text"])
	}
}

// An image fetched through the text path reaches perception byte for byte;
// text normalization would strip format runes and corrupt the raster.
func TestFetchURLTextModeImageKeepsRawBytes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	allowLoopbackFetch(t)

	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	var buf bytes.Buffer
	testutil.FailErr(t, "encode png", png.Encode(&buf, img))
	// A tEXt chunk carrying U+200B, which text hygiene strips.
	body := insertPNGText(t, buf.Bytes(), "Comment\x00zero\u200bwidth")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "RegisterToolsWithFactory", RegisterToolsWithFactory(reg, ToolDeps{Boundary: rawTestBoundary(t)}, nil))
	tctx := tools.ToolContext{SessionID: "s1", ToolCallID: "call1", Out: &tools.ToolInvocationOut{}}
	_, err := reg.Run(context.Background(), "fetch_url", map[string]any{"url": srv.URL + "/render"}, tctx)
	testutil.FailErr(t, "fetch_url", err)
	if tctx.Out.Visual == nil || !bytes.Equal(tctx.Out.Visual.Bytes, body) {
		t.Fatal("fetched image bytes changed before perception")
	}
}

func insertPNGText(t *testing.T, raw []byte, text string) []byte {
	t.Helper()
	iend := bytes.Index(raw, []byte("IEND")) - 4
	data := []byte(text)
	var out bytes.Buffer
	out.Write(raw[:iend])
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(data)))
	out.Write(n[:])
	out.WriteString("tEXt")
	out.Write(data)
	crc := crc32.NewIEEE()
	crc.Write([]byte("tEXt"))
	crc.Write(data)
	binary.BigEndian.PutUint32(n[:], crc.Sum32())
	out.Write(n[:])
	out.Write(raw[iend:])
	return out.Bytes()
}

func TestFetchURLNonImageBinaryRequiresDest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	allowLoopbackFetch(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/font-woff2")
		_, _ = w.Write([]byte("fake-font-data"))
	}))
	defer srv.Close()

	tempDir := t.TempDir()
	reg := tools.NewDefaultRegistry()
	deps := ToolDeps{Boundary: rawTestBoundary(t)}
	testutil.FailErr(t, "RegisterToolsWithFactory", RegisterToolsWithFactory(reg, deps, nil))

	tctx := tools.ToolContext{
		Roots: []projectroot.RootRef{
			{ID: "r1", Path: tempDir, IsPrimary: true},
		},
		ActiveRootID: "r1",
		SessionID:    "s1",
		ToolCallID:   "call1",
		Out:          &tools.ToolInvocationOut{},
	}

	_, err := reg.Run(context.Background(), "fetch_url", map[string]any{
		"url":  srv.URL,
		"mode": "raw",
	}, tctx)
	if err == nil {
		t.Fatal("expected FETCH_URL_DEST_REQUIRED error")
	}
	var rej *tools.ToolReject
	if !errors.As(err, &rej) || rej.Code != "FETCH_URL_DEST_REQUIRED" {
		t.Fatalf("expected FETCH_URL_DEST_REQUIRED, got: %#v", err)
	}
}

func TestFetchURLImageSecretScreeningWithheld(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	allowLoopbackFetch(t)

	secretSVG := `<svg><text>AKIAQYJK5TXV4NZR7SGB</text></svg>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write([]byte(secretSVG))
	}))
	defer srv.Close()

	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build matcher", err)

	tempDir := t.TempDir()
	reg := tools.NewDefaultRegistry()
	gate := visualscreen.NewGate(visualscreen.NewScanner(nil), matcher, func(ctx context.Context, alert secretmatch.Alert) (secretmatch.Resolution, error) {
		return secretmatch.Resolution{Decision: secretmatch.Withhold}, nil
	})

	deps := ToolDeps{
		Boundary:     rawTestBoundary(t),
		VisualScreen: gate,
	}
	testutil.FailErr(t, "RegisterToolsWithFactory", RegisterToolsWithFactory(reg, deps, nil))

	tctx := tools.ToolContext{
		Roots: []projectroot.RootRef{
			{ID: "r1", Path: tempDir, IsPrimary: true},
		},
		ActiveRootID: "r1",
		SessionID:    "s1",
		ToolCallID:   "call1",
		Out:          &tools.ToolInvocationOut{},
	}

	_, err = reg.Run(context.Background(), "fetch_url", map[string]any{
		"url": srv.URL,
	}, tctx)
	if err == nil {
		t.Fatal("expected secret withheld reject")
	}
	var rej *tools.ToolReject
	if !errors.As(err, &rej) || rej.Code != "FETCH_URL_SECRET_WITHHELD" {
		t.Fatalf("expected FETCH_URL_SECRET_WITHHELD, got: %#v", err)
	}
}
