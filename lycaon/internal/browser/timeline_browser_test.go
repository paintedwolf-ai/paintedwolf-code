package browser

import (
	"bytes"
	"errors"
	"image/png"
	"testing"

	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/timelinearchive"
)

// A click that inserts a notice at once, then 800 ms later a banner that pushes the content
// down: the kind of late shift a settled screenshot cannot show.
const shiftingPage = `<!doctype html>
<style>body{margin:0;font:16px sans-serif} #banner{height:120px;background:#c33;color:white}</style>
<button id="go">Load</button>
<div id="content" style="height:200px;background:#ddd">content</div>
<script>
  document.getElementById("go").addEventListener("click", () => {
    const notice = document.createElement("div");
    notice.textContent = "loading";
    document.body.insertBefore(notice, document.getElementById("content"));
    setTimeout(() => {
      const b = document.createElement("div");
      b.id = "banner";
      b.textContent = "late banner";
      document.body.insertBefore(b, document.getElementById("content"));
    }, 800);
  });
</script>`

func TestRecordingShowsALateLayoutShiftASettledCaptureWouldMiss(t *testing.T) {
	held := openHeldHTML(t, shiftingPage)
	tail := 1500
	rec, err := held.Record(t.Context(), []CaptureAction{{Type: "click", ActionLocator: ActionLocator{Selector: "#go"}}},
		RecordOpts{Watch: []string{"#content"}, TailMS: &tail}, nil)
	testutil.FailErr(t, "record", err)
	s := rec.Summary
	if s.LayoutShift.Count == 0 || s.LayoutShift.Total <= 0 || s.LayoutShift.AfterInput.Count == 0 {
		t.Fatalf("layout shifts = %+v, want the late banner as unexpected and the notice as input-driven", s.LayoutShift)
	}
	if len(s.Watch) != 1 || len(s.Watch[0].Jumps) == 0 || s.Watch[0].MaxDisplacementPX < 100 {
		t.Fatalf("watched content did not jump: %+v", s.Watch)
	}
	jump := s.Watch[0].Jumps[len(s.Watch[0].Jumps)-1]
	if jump.DY < 100 || jump.AtMS < 700 {
		t.Fatalf("last jump = %+v, want the banner's push about 800 ms in", jump)
	}
	if len(s.VisualChanges) == 0 || s.VisuallyStableAtMS < jump.AtMS || s.StillChangingAtEnd {
		t.Fatalf("stability = %v changes %+v", s.VisuallyStableAtMS, s.VisualChanges)
	}
	if len(rec.Actions) != 1 || !rec.Actions[0].OK || rec.Actions[0].StartMS > jump.AtMS {
		t.Fatalf("actions = %+v", rec.Actions)
	}
	m, entries, err := timelinearchive.Unpack(rec.Archive)
	testutil.FailErr(t, "unpack timeline", err)
	if len(m.Frames) != s.FrameCount || len(m.Frames) < 3 || m.Frames[0].AtMS > 100 {
		t.Fatalf("frames = %d (summary %d), first at %v", len(m.Frames), s.FrameCount, m.Frames[0].AtMS)
	}
	for i, f := range m.Frames {
		if len(entries[f.File]) == 0 || (i > 0 && f.AtMS < m.Frames[i-1].AtMS) {
			t.Fatalf("frame %d = %+v", i, f)
		}
	}
	poster, err := timelinearchive.Poster(rec.Archive)
	testutil.FailErr(t, "read poster", err)
	img, err := png.Decode(bytes.NewReader(poster))
	testutil.FailErr(t, "decode poster", err)
	if img.Bounds().Dx() > 2048 || len(s.Sheet) < 3 || s.Sheet[0].Why != "start" {
		t.Fatalf("poster %v sheet %+v", img.Bounds(), s.Sheet)
	}
	for _, c := range s.Sheet {
		if c.Frame >= len(m.Frames) || m.Frames[c.Frame].AtMS != c.AtMS {
			t.Fatalf("sheet cell %+v does not name an archived frame", c)
		}
	}
}

func TestRecordingSurvivesTheStepThatFails(t *testing.T) {
	held := openHeldHTML(t, shiftingPage)
	tail := 1000
	rec, err := held.Record(t.Context(), []CaptureAction{
		{Type: "click", ActionLocator: ActionLocator{Selector: "#go"}},
		{Type: "click", ActionLocator: ActionLocator{Selector: "#missing"}, TimeoutMS: 200},
	}, RecordOpts{Watch: []string{"#content"}, TailMS: &tail}, nil)
	var rej *browserengine.RejectError
	if !errors.As(err, &rej) || rej.Code != "CAPTURE_ACTION_FAILED" || rej.Data["index"] != 1 {
		t.Fatalf("a missing target did not reject the step: %v", err)
	}
	if !rec.Recorded() || rec.Summary.FrameCount == 0 || len(rec.Results) != 1 {
		t.Fatalf("the recording made before the failure was lost: frames=%d results=%d", rec.Summary.FrameCount, len(rec.Results))
	}
	if len(rec.Actions) != 2 || !rec.Actions[0].OK || rec.Actions[1].OK {
		t.Fatalf("actions = %+v, want the first ok and the second failed", rec.Actions)
	}
	// The tail after the failed step still catches the late banner the first click caused.
	if len(rec.Summary.Watch) != 1 || rec.Summary.Watch[0].MaxDisplacementPX < 100 {
		t.Fatalf("the tail after the failure did not record the late shift: %+v", rec.Summary.Watch)
	}
}

func TestRecordingIsItsOwnSessionBesideTheLivePreview(t *testing.T) {
	held := openHeldHTML(t, shiftingPage)
	zero := 0
	for range 2 {
		rec, err := held.Record(t.Context(), []CaptureAction{{Type: "click", ActionLocator: ActionLocator{Selector: "#go"}}}, RecordOpts{TailMS: &zero}, nil)
		testutil.FailErr(t, "record", err)
		if rec.Summary.FrameCount == 0 {
			t.Fatal("a repeated recording kept no frames")
		}
	}
	// The page stays drivable after its recorder sessions detach.
	_, err := held.Act(t.Context(), []CaptureAction{{Type: "click", ActionLocator: ActionLocator{Selector: "#go"}}}, nil)
	testutil.FailErr(t, "drive after recording", err)
}

func TestMeasureReportsWhetherThePointerCanReachEachElement(t *testing.T) {
	held := openHeldHTML(t, `<!doctype html>
<style>body{margin:0} button{display:block;width:160px;height:40px}</style>
<button id="ok">Reachable</button>
<button id="covered">Covered</button>
<div id="scrim" style="position:fixed;top:40px;left:0;width:300px;height:40px"></div>
<div id="list" style="height:60px;overflow:hidden"><div style="height:200px"></div><button id="clipped">Clipped</button></div>`)
	out, err := MeasureHeld(t.Context(), held, "page-1", MeasureRequest{Selectors: []string{"#ok", "#covered", "#clipped"}})
	testutil.FailErr(t, "measure", err)
	states := map[string]PointerReach{}
	for _, el := range out.Elements {
		states[el.Selector] = el.Reach
	}
	if states["#ok"].State != ReachReceives || states["#ok"].PointsReceiving != 5 {
		t.Fatalf("#ok = %+v", states["#ok"])
	}
	if c := states["#covered"]; c.State != ReachCovered || c.CoveredBy == nil || c.CoveredBy.ID != "scrim" {
		t.Fatalf("#covered = %+v", c)
	}
	if c := states["#clipped"]; c.State != ReachClipped || c.ClippedBy == nil || c.ClippedBy.ID != "list" {
		t.Fatalf("#clipped = %+v", c)
	}
	blocked := map[string]bool{}
	for _, r := range out.Relations {
		if r.Kind == "pointer_blocked" {
			blocked[r.Of] = true
		}
	}
	if !blocked["#covered"] || !blocked["#clipped"] || blocked["#ok"] {
		t.Fatalf("pointer_blocked relations = %v", blocked)
	}
}
