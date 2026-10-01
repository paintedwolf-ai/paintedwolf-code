package browser

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/timelinearchive"
)

// solidFrame is a 160×90 frame filled with one gray level, with the given top rows painted white.
func solidFrame(level uint8, whiteRows int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 160, 90))
	for y := range 90 {
		for x := range 160 {
			c := color.RGBA{R: level, G: level, B: level, A: 255}
			if y < whiteRows {
				c = color.RGBA{R: 255, G: 255, B: 255, A: 255}
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func framesFrom(t *testing.T, specs []struct {
	at    float64
	white int
}) []screenedFrame {
	t.Helper()
	out := make([]screenedFrame, 0, len(specs))
	var prev *luma
	for _, s := range specs {
		img := solidFrame(40, s.white)
		grid := lumaGrid(img)
		f := screenedFrame{atMS: s.at, image: img}
		if prev != nil {
			f.changed = grid.changedFraction(prev)
		}
		prev = grid
		out = append(out, f)
	}
	return out
}

func TestChangedFractionMeasuresTheShareOfTheViewportThatRepainted(t *testing.T) {
	a, b := lumaGrid(solidFrame(40, 0)), lumaGrid(solidFrame(40, 45))
	if got := b.changedFraction(a); math.Abs(got-0.5) > 0.02 {
		t.Fatalf("half the frame repainted, fraction = %v", got)
	}
	if got := a.changedFraction(a); got != 0 {
		t.Fatalf("identical frames changed %v", got)
	}
}

func TestSummaryDerivesStabilityShiftsTasksRequestsAndWatchedJumps(t *testing.T) {
	frames := framesFrom(t, []struct {
		at    float64
		white int
	}{{0, 0}, {120, 0}, {420, 30}, {460, 60}, {900, 60}, {1400, 60}})
	m := timelinearchive.Manifest{
		DurationMS: 1500,
		Actions:    []timelinearchive.Action{{Index: 0, Type: "click", Label: "click #go", StartMS: 50, EndMS: 150, OK: true}},
		Events: []timelinearchive.Event{
			{AtMS: 110, Kind: "layout_shift", Detail: map[string]any{"value": 0.3, "had_recent_input": true}},
			{AtMS: 430, Kind: "layout_shift", Detail: map[string]any{"value": 0.12, "sources": []any{"banner"}}},
			{AtMS: 470, Kind: "layout_shift", Detail: map[string]any{"value": 0.05}},
			{AtMS: 200, Kind: "long_task", Detail: map[string]any{"duration_ms": 80.0}},
			{AtMS: 300, Kind: "request", Detail: map[string]any{"status": 200.0}},
			{AtMS: 310, Kind: "request", Detail: map[string]any{"status": 500.0}},
			{AtMS: 320, Kind: "request", Detail: map[string]any{"failure": "net::ERR_FAILED"}},
			{AtMS: 330, Kind: "error", Detail: map[string]any{"message": "boom"}},
		},
		Watch: []timelinearchive.WatchTrack{{Selector: "#content", Samples: []timelinearchive.WatchSample{
			{AtMS: 0, Present: true, Box: &timelinearchive.Box{X: 0, Y: 100, Width: 800, Height: 50}},
			{AtMS: 430, Present: true, Box: &timelinearchive.Box{X: 0, Y: 220, Width: 800, Height: 50}},
			{AtMS: 431, Present: true, Box: &timelinearchive.Box{X: 0, Y: 220.4, Width: 800, Height: 50}},
		}}},
	}
	s := summarize(m, frames)
	if s.VisuallyStableAtMS != 460 || s.StillChangingAtEnd || len(s.VisualChanges) != 2 {
		t.Fatalf("stability = %v still=%v changes=%+v", s.VisuallyStableAtMS, s.StillChangingAtEnd, s.VisualChanges)
	}
	if s.LayoutShift.Count != 2 || s.LayoutShift.Total != 0.17 || s.LayoutShift.Worst.AtMS != 430 || len(s.LayoutShift.Worst.Sources) != 1 {
		t.Fatalf("layout shift = %+v (input-driven shifts are totalled apart)", s.LayoutShift)
	}
	if s.LayoutShift.AfterInput != (timelinearchive.ShiftTotal{Total: 0.3, Count: 1}) {
		t.Fatalf("input-driven shifts = %+v", s.LayoutShift.AfterInput)
	}
	if s.LongTasks != (timelinearchive.LongTasks{Count: 1, TotalMS: 80, MaxMS: 80}) {
		t.Fatalf("long tasks = %+v", s.LongTasks)
	}
	if s.Requests != 3 || s.FailedRequests != 2 || s.Errors != 1 {
		t.Fatalf("requests=%d failed=%d errors=%d", s.Requests, s.FailedRequests, s.Errors)
	}
	w := s.Watch[0]
	if len(w.Jumps) != 1 || w.Jumps[0].DY != 120 || w.MaxDisplacementPX != 120.4 || w.SettledMS != 430 || !w.Present {
		t.Fatalf("watch = %+v", w)
	}
}

func TestSheetShowsStartEndActionsAndChangesWithinItsCellCount(t *testing.T) {
	specs := make([]struct {
		at    float64
		white int
	}, 40)
	for i := range specs {
		specs[i].at = float64(i * 50)
		// The page repaints every frame, then holds still for the last ten.
		specs[i].white = (min(i, 29) % 7) * 10
	}
	frames := framesFrom(t, specs)
	m := timelinearchive.Manifest{DurationMS: 2000, Actions: []timelinearchive.Action{{Label: "click #a", EndMS: 300}, {Label: "type hello", EndMS: 800}}}
	s := summarize(m, frames)
	if len(s.Sheet) != sheetCells {
		t.Fatalf("sheet has %d cells, want %d", len(s.Sheet), sheetCells)
	}
	whys := map[string]bool{}
	for i, c := range s.Sheet {
		whys[c.Why] = true
		if i > 0 && c.Frame <= s.Sheet[i-1].Frame {
			t.Fatalf("sheet is not in frame order: %+v", s.Sheet)
		}
	}
	for _, want := range []string{"start", "end", "after click #a", "after type hello", "last change"} {
		if !whys[want] {
			t.Fatalf("sheet lacks %q: %+v", want, s.Sheet)
		}
	}
}

func TestFramesInWindowStartsFromThePaintOnScreenWhenRecordingBegan(t *testing.T) {
	frames := []recordedFrame{{wallMS: 90}, {wallMS: 95}, {wallMS: 120}, {wallMS: 300}, {wallMS: 900}}
	got := framesInWindow(frames, 100, 500)
	if len(got) != 3 || got[0].wallMS != 95 || got[2].wallMS != 300 {
		t.Fatalf("window = %+v", got)
	}
	thin := thinFrames(make([]recordedFrame, 1000), 300)
	if len(thin) != 300 {
		t.Fatalf("thinned to %d", len(thin))
	}
}

func TestBracketingMaskCoversTextOnEitherSideOfAFrame(t *testing.T) {
	samples := []regionSample{{wallMS: 100}, {wallMS: 200}, {wallMS: 300}}
	masks := make([]captureprojection.RasterMask, 3)
	if _, ok := bracketingMask(nil, nil, 150); ok {
		t.Fatal("a frame with no screening samples was allowed")
	}
	for _, at := range []float64{50, 150, 250, 350} {
		if _, ok := bracketingMask(samples, masks, at); !ok {
			t.Fatalf("frame at %v had no mask", at)
		}
	}
}
