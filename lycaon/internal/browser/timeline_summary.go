package browser

import (
	"fmt"
	"image"
	"math"
	"sort"

	"github.com/lycaon/lycaon/internal/timelinearchive"
)

const (
	lumaCols = 96
	lumaRows = 54
	// lumaCellDelta is the brightness step that counts a sample point as changed.
	lumaCellDelta = 16
	// visualChangeFloor ignores changes below this share of the viewport, such as a blinking caret.
	visualChangeFloor = 0.002
	// stillChangingWindowMS: a change this close to the end means the page had not settled.
	stillChangingWindowMS = 150
	maxReportedChanges    = 10
	maxWatchJumps         = 12
	jumpFloorPX           = 1
	sheetCells            = 12
	sheetColumns          = 4
)

// luma is a coarse brightness grid of a frame, enough to tell where paint changed.
type luma [lumaCols * lumaRows]uint8

func lumaGrid(img image.Image) *luma {
	var g luma
	b := img.Bounds()
	for row := range lumaRows {
		y := b.Min.Y + (2*row+1)*b.Dy()/(2*lumaRows)
		for col := range lumaCols {
			x := b.Min.X + (2*col+1)*b.Dx()/(2*lumaCols)
			r, gr, bl, _ := img.At(x, y).RGBA()
			g[row*lumaCols+col] = uint8((299*r + 587*gr + 114*bl) / 1000 >> 8) //nolint:gosec // G115 — RGBA channels are at most 0xffff, so the weighted mean shifted by 8 fits in a byte
		}
	}
	return &g
}

func (g *luma) changedFraction(prev *luma) float64 {
	changed := 0
	for i := range g {
		if d := int(g[i]) - int(prev[i]); d > lumaCellDelta || d < -lumaCellDelta {
			changed++
		}
	}
	return float64(changed) / float64(len(g))
}

func timelineEvents(in timelineInput) []timelinearchive.Event {
	var events []timelinearchive.Event
	for _, t := range in.telemetry {
		if t.kind == "watch" || t.kind == telemetryGeometry {
			continue
		}
		events = append(events, timelinearchive.Event{AtMS: in.at(t.wallMS), Kind: t.kind, Detail: t.body})
	}
	for _, r := range in.network {
		detail := map[string]any{"method": r.Method, "url": r.URL}
		if r.Type != "" {
			detail["type"] = r.Type
		}
		if r.Status != 0 {
			detail["status"] = r.Status
		}
		if r.Failure != "" {
			detail["failure"] = r.Failure
		}
		if r.DurationMS != 0 {
			detail["duration_ms"] = r.DurationMS
		}
		if r.ServedBy != "" {
			detail["served_by"] = r.ServedBy
		}
		if r.Route != nil {
			detail["route"] = *r.Route
		}
		if r.Pending {
			detail["pending"] = true
		}
		events = append(events, timelinearchive.Event{AtMS: in.at(r.wallMS), Kind: "request", Detail: detail})
	}
	for _, e := range in.errors {
		detail := map[string]any{"message": e.Message}
		if e.Source != "" {
			detail["source"] = e.Source
		}
		events = append(events, timelinearchive.Event{AtMS: in.at(e.wallMS), Kind: "error", Detail: detail})
	}
	for _, l := range in.console {
		events = append(events, timelinearchive.Event{AtMS: in.at(l.wallMS), Kind: "console", Detail: map[string]any{"line": l.line}})
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].AtMS < events[j].AtMS })
	return events
}

func watchTracks(in timelineInput) []timelinearchive.WatchTrack {
	tracks := make([]timelinearchive.WatchTrack, len(in.watch))
	index := map[string]int{}
	for i, sel := range in.watch {
		tracks[i].Selector = sel
		index[sel] = i
	}
	for _, t := range in.telemetry {
		if t.kind != "watch" {
			continue
		}
		sel, _ := t.body["selector"].(string)
		i, ok := index[sel]
		if !ok {
			continue
		}
		sample := timelinearchive.WatchSample{AtMS: in.at(t.wallMS)}
		sample.Present, _ = t.body["present"].(bool)
		if sample.Present {
			sample.Box = &timelinearchive.Box{X: num(t.body["x"]), Y: num(t.body["y"]), Width: num(t.body["width"]), Height: num(t.body["height"])}
		}
		if v, ok := t.body["scroll_top"].(float64); ok {
			sample.ScrollTop = &v
		}
		if v, ok := t.body["scroll_left"].(float64); ok {
			sample.ScrollLeft = &v
		}
		tracks[i].Samples = append(tracks[i].Samples, sample)
	}
	return tracks
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

func summarize(m timelinearchive.Manifest, frames []screenedFrame) timelinearchive.Summary {
	s := timelinearchive.Summary{DurationMS: m.DurationMS, FrameCount: len(frames)}
	var changes []timelinearchive.VisualChange
	for _, f := range frames {
		if f.changed > visualChangeFloor {
			changes = append(changes, timelinearchive.VisualChange{AtMS: f.atMS, Fraction: math.Round(f.changed*1000) / 1000})
			s.VisuallyStableAtMS = f.atMS
		}
	}
	s.StillChangingAtEnd = len(changes) > 0 && m.DurationMS-s.VisuallyStableAtMS <= stillChangingWindowMS
	s.VisualChanges = largestChanges(changes)
	for _, e := range m.Events {
		switch e.Kind {
		case "layout_shift":
			value := num(e.Detail["value"])
			if recent, _ := e.Detail["had_recent_input"].(bool); recent {
				s.LayoutShift.AfterInput.Total = math.Round((s.LayoutShift.AfterInput.Total+value)*10000) / 10000
				s.LayoutShift.AfterInput.Count++
				continue
			}
			s.LayoutShift.Total = math.Round((s.LayoutShift.Total+value)*10000) / 10000
			s.LayoutShift.Count++
			if s.LayoutShift.Worst == nil || value > s.LayoutShift.Worst.Value {
				sources, _ := e.Detail["sources"].([]any)
				s.LayoutShift.Worst = &timelinearchive.ShiftAtTime{AtMS: e.AtMS, Value: value, Sources: sources}
			}
		case "long_task":
			d := num(e.Detail["duration_ms"])
			s.LongTasks.Count++
			s.LongTasks.TotalMS = roundTenth(s.LongTasks.TotalMS + d)
			s.LongTasks.MaxMS = max(s.LongTasks.MaxMS, d)
		case "request":
			s.Requests++
			if status := num(e.Detail["status"]); status >= 400 || e.Detail["failure"] != nil {
				s.FailedRequests++
			}
		case "error":
			s.Errors++
		}
	}
	for _, track := range m.Watch {
		s.Watch = append(s.Watch, summarizeWatch(track))
	}
	s.Sheet = chooseSheet(frames, m, s)
	return s
}

// largestChanges keeps the biggest paint changes, reported in time order.
func largestChanges(changes []timelinearchive.VisualChange) []timelinearchive.VisualChange {
	if len(changes) <= maxReportedChanges {
		return changes
	}
	ranked := append([]timelinearchive.VisualChange(nil), changes...)
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Fraction > ranked[j].Fraction })
	ranked = ranked[:maxReportedChanges]
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].AtMS < ranked[j].AtMS })
	return ranked
}

func summarizeWatch(track timelinearchive.WatchTrack) timelinearchive.WatchSummary {
	out := timelinearchive.WatchSummary{Selector: track.Selector}
	var prev *timelinearchive.WatchSample
	var jumps []timelinearchive.Jump
	for i := range track.Samples {
		cur := &track.Samples[i]
		if cur.Box != nil && out.First == nil {
			out.First = cur.Box
		}
		if cur.Box != nil && out.First != nil {
			out.MaxDisplacementPX = max(out.MaxDisplacementPX, roundTenth(math.Hypot(cur.Box.X-out.First.X, cur.Box.Y-out.First.Y)))
		}
		if prev != nil {
			if j, ok := jumpBetween(prev, cur); ok {
				jumps = append(jumps, j)
				out.SettledMS = cur.AtMS
			}
		}
		prev = cur
	}
	if prev != nil {
		out.Present = prev.Present
		out.Last = prev.Box
	}
	if len(jumps) > maxWatchJumps {
		sort.SliceStable(jumps, func(i, j int) bool { return jumpSize(jumps[i]) > jumpSize(jumps[j]) })
		jumps = jumps[:maxWatchJumps]
		sort.SliceStable(jumps, func(i, j int) bool { return jumps[i].AtMS < jumps[j].AtMS })
	}
	out.Jumps = jumps
	return out
}

func jumpBetween(a, b *timelinearchive.WatchSample) (timelinearchive.Jump, bool) {
	j := timelinearchive.Jump{AtMS: b.AtMS}
	if a.Box != nil && b.Box != nil {
		j.DX, j.DY = roundTenth(b.Box.X-a.Box.X), roundTenth(b.Box.Y-a.Box.Y)
		j.DW, j.DH = roundTenth(b.Box.Width-a.Box.Width), roundTenth(b.Box.Height-a.Box.Height)
	}
	if a.ScrollTop != nil && b.ScrollTop != nil {
		j.Scroll = roundTenth(*b.ScrollTop - *a.ScrollTop)
	}
	appeared := a.Present != b.Present
	return j, appeared || jumpSize(j) > jumpFloorPX
}

func jumpSize(j timelinearchive.Jump) float64 {
	return max(math.Abs(j.DX), math.Abs(j.DY), math.Abs(j.DW), math.Abs(j.DH), math.Abs(j.Scroll))
}

// chooseSheet picks the frames the contact sheet shows: the start, the moment after each
// action, the largest changes, the point the page went still, and the end, then even fill.
func chooseSheet(frames []screenedFrame, m timelinearchive.Manifest, s timelinearchive.Summary) []timelinearchive.SheetCell {
	type want struct {
		at  float64
		why string
	}
	wants := []want{{0, "start"}, {m.DurationMS, "end"}}
	if len(s.VisualChanges) > 0 {
		wants = append(wants, want{s.VisuallyStableAtMS, "last change"})
	}
	for _, a := range m.Actions {
		wants = append(wants, want{a.EndMS, "after " + a.Label})
	}
	ranked := append([]timelinearchive.VisualChange(nil), s.VisualChanges...)
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Fraction > ranked[j].Fraction })
	for _, c := range ranked {
		wants = append(wants, want{c.AtMS, fmt.Sprintf("changed %.0f%%", c.Fraction*100)})
	}
	chosen := map[int]string{}
	for _, w := range wants {
		if len(chosen) == sheetCells {
			break
		}
		i := frameAt(frames, w.at)
		if _, taken := chosen[i]; !taken {
			chosen[i] = w.why
		}
	}
	// Fill evenly, then with any frame not yet shown, until the sheet is full.
	fill := make([]int, 0, sheetCells+len(frames))
	for n := range sheetCells {
		fill = append(fill, n*(len(frames)-1)/(sheetCells-1))
	}
	for i := range frames {
		fill = append(fill, i)
	}
	for _, i := range fill {
		if len(chosen) == min(sheetCells, len(frames)) {
			break
		}
		if _, taken := chosen[i]; !taken {
			chosen[i] = "t"
		}
	}
	cells := make([]timelinearchive.SheetCell, 0, len(chosen))
	for i, why := range chosen {
		cells = append(cells, timelinearchive.SheetCell{AtMS: frames[i].atMS, Frame: i, Why: why})
	}
	sort.Slice(cells, func(i, j int) bool { return cells[i].Frame < cells[j].Frame })
	return cells
}

// frameAt is the frame on screen at a time: the last one painted at or before it.
func frameAt(frames []screenedFrame, at float64) int {
	i := sort.Search(len(frames), func(i int) bool { return frames[i].atMS > at })
	return max(0, i-1)
}
