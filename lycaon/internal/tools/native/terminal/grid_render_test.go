package terminal

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRenderTerminalGridPNG(t *testing.T) {
	screen := bgprocess.ScreenSnapshot{
		Cols:      40,
		Rows:      5,
		CursorCol: 0,
		CursorRow: 4,
		Lines: []string{
			"Process Summary",
			"────────────────────────────────────────",
			"~  ████████████░░░░░░░░░░░░   1",
			"↻ Recurring: next occurrence (weekly)",
			"",
		},
	}
	mime, raw, w, h, err := renderTerminalGridPNG(screen)
	testutil.FailErr(t, "render", err)
	if mime != "image/png" {
		t.Fatalf("mime = %q", mime)
	}
	if len(raw) == 0 {
		t.Fatal("empty png")
	}
	if w <= 0 || h <= 0 {
		t.Fatalf("dims = %dx%d", w, h)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	testutil.FailErr(t, "decode png", err)
	if img.Bounds().Dx() != w || img.Bounds().Dy() != h {
		t.Fatalf("decoded %vx%v want %dx%d", img.Bounds().Dx(), img.Bounds().Dy(), w, h)
	}
}

func TestRenderTerminalGridEmpty(t *testing.T) {
	mime, raw, w, h, err := renderTerminalGridPNG(bgprocess.ScreenSnapshot{})
	testutil.FailErr(t, "render empty", err)
	if mime != "" || len(raw) != 0 || w != 0 || h != 0 {
		t.Fatalf("unexpected output for empty screen: %q, len=%d, %dx%d", mime, len(raw), w, h)
	}
}
