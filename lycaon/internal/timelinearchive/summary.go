package timelinearchive

// Summary is what a recording shows, derived by the host from frames and events so the
// facts do not depend on reading pixels.
type Summary struct {
	DurationMS float64 `json:"duration_ms"`
	FrameCount int     `json:"frame_count"`
	// VisuallyStableAtMS is when the last painted change happened; the page looked the
	// same from then to the end of the recording.
	VisuallyStableAtMS float64 `json:"visually_stable_at_ms"`
	// StillChangingAtEnd says the page was still painting changes when recording stopped.
	StillChangingAtEnd bool           `json:"still_changing_at_end,omitempty"`
	VisualChanges      []VisualChange `json:"visual_changes,omitempty"`
	LayoutShift        LayoutShift    `json:"layout_shift"`
	LongTasks          LongTasks      `json:"long_tasks"`
	Watch              []WatchSummary `json:"watch,omitempty"`
	Requests           int            `json:"requests"`
	FailedRequests     int            `json:"failed_requests,omitempty"`
	Errors             int            `json:"errors,omitempty"`
	// Sheet lists the frame each contact sheet cell shows, in reading order.
	Sheet []SheetCell `json:"sheet"`
}

// Report is what a drive's caller reads: the summary, and when each action ran so its
// times line up with the summary's.
type Report struct {
	Summary
	Actions []Action `json:"actions"`
}

// VisualChange is a frame whose paint differed from the one before it.
type VisualChange struct {
	AtMS float64 `json:"at_ms"`
	// Fraction is the share of the viewport that changed, from 0 to 1.
	Fraction float64 `json:"fraction"`
}

// LayoutShift totals layout movement from the browser's own layout-shift scores. Total and
// Count are the unexpected shifts a page's cumulative layout shift counts; shifts within the
// browser's window after input are totalled apart, since the drive's own input caused them.
type LayoutShift struct {
	Total      float64      `json:"total"`
	Count      int          `json:"count"`
	Worst      *ShiftAtTime `json:"worst,omitempty"`
	AfterInput ShiftTotal   `json:"after_input"`
}

// ShiftTotal sums a set of layout shifts.
type ShiftTotal struct {
	Total float64 `json:"total"`
	Count int     `json:"count"`
}

// ShiftAtTime is one layout shift and the elements that moved in it.
type ShiftAtTime struct {
	AtMS    float64 `json:"at_ms"`
	Value   float64 `json:"value"`
	Sources []any   `json:"sources,omitempty"`
}

// LongTasks totals main-thread tasks the browser reported as long.
type LongTasks struct {
	Count   int     `json:"count"`
	TotalMS float64 `json:"total_ms"`
	MaxMS   float64 `json:"max_ms"`
}

// WatchSummary is how a watched element moved.
type WatchSummary struct {
	Selector string `json:"selector"`
	// Present says whether the element existed at the end of the recording.
	Present bool `json:"present"`
	First   *Box `json:"first,omitempty"`
	Last    *Box `json:"last,omitempty"`
	// MaxDisplacementPX is the farthest the element's top-left corner moved from where it started.
	MaxDisplacementPX float64 `json:"max_displacement_px"`
	// Jumps are moves larger than a pixel between consecutive samples.
	Jumps     []Jump  `json:"jumps,omitempty"`
	SettledMS float64 `json:"settled_at_ms"`
}

// Jump is one change in a watched element's box or scroll offset.
type Jump struct {
	AtMS   float64 `json:"at_ms"`
	DX     float64 `json:"dx,omitempty"`
	DY     float64 `json:"dy,omitempty"`
	DW     float64 `json:"dw,omitempty"`
	DH     float64 `json:"dh,omitempty"`
	Scroll float64 `json:"scroll,omitempty"`
}

// SheetCell names the frame one contact sheet cell shows.
type SheetCell struct {
	AtMS  float64 `json:"at_ms"`
	Frame int     `json:"frame"`
	Why   string  `json:"why"`
}
