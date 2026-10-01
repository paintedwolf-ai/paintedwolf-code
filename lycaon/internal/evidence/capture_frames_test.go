package evidence

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCaptureFrameHighlightRecords_mintsFrameIndexedPages(t *testing.T) {
	content := `{"capture":"filmstrip","mime":"application/vnd.lycaon.filmstrip+zip","frames":[
		{"index":0,"caption":"initial","state":{"url":"/"},"snapshot":{"text":"loading"}},
		{"index":1,"caption":"click #load","state":{"url":"/"},"snapshot":{"text":"alpha beta gamma"}}
	],"state":{},"snapshot":{}}`
	recs := CaptureFrameHighlightRecords(content)
	if len(recs) != 2 {
		t.Fatalf("recs=%d", len(recs))
	}
	if recs[0].FrameIndex != 1 || recs[1].FrameIndex != 2 {
		t.Fatalf("frame_index = %d,%d want 1,2", recs[0].FrameIndex, recs[1].FrameIndex)
	}
	if recs[0].Shape != ShapeSurfaceSnapshot || recs[0].Surface != SurfaceDOM {
		t.Fatalf("shape/surface %#v", recs[0])
	}
	ok, detail := VerifyRecord(recs[1], Claim{Excerpt: "alpha beta gamma"})
	if !ok {
		t.Fatalf("expected post-frame body to verify gamma excerpt; body=%q detail=%q", recs[1].Body, detail)
	}
}

func TestCaptureFrameHighlightRecords_skipsScreenshot(t *testing.T) {
	content := `{"capture":"screenshot","mime":"image/png","state":{},"snapshot":{}}`
	if recs := CaptureFrameHighlightRecords(content); len(recs) != 0 {
		t.Fatalf("screenshot must not mint frames: %d", len(recs))
	}
}

func TestPatchCaptureFrameHandles(t *testing.T) {
	in := `{"capture":"filmstrip","frames":[{"index":0,"caption":"initial","state":{},"snapshot":{}},{"index":1,"caption":"click","state":{},"snapshot":{}}]}`
	out, err := PatchCaptureFrameHandles(in, []string{"page#1", "page#2"})
	testutil.FailErr(t, "PatchCaptureFrameHandles failed", err)
	if !strings.Contains(out, `"evidence_handle":"page#1"`) || !strings.Contains(out, `"evidence_handle":"page#2"`) {
		t.Fatalf("patched = %s", out)
	}
}

func TestBuildEvidenceRecord_screenshotHasNoFrameIndex(t *testing.T) {
	rec := BuildEvidenceRecord("/tmp/proj", "capture_page", map[string]any{"url": "http://x"}, `{"log":["ok"],"snapshot":{}}`)
	if rec.FrameIndex != 0 {
		t.Fatalf("FrameIndex=%d want 0", rec.FrameIndex)
	}
}
