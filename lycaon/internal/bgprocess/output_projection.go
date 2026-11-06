package bgprocess

import (
	"context"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/pkg/api"
)

// captureUnavailableText stands in for output that could not be screened.
const captureUnavailableText = "[Capture screening unavailable]"

// safeProjection is one screened view of a process's retained output window.
type safeProjection struct {
	Text   string
	Chunks []OutputChunk
	// Head is the cursor of the first byte the projection covers. It sits past
	// the buffer head when a withheld range straddled it.
	Head      int64
	Next      int64
	Truncated bool
	// Screened is false when the window could not be projected at all.
	Screened bool
	// Empty is true when the buffer holds nothing to project.
	Empty bool
}

// projectWindow screens retained bytes together and drops masked tails.
//
// Callers hold proc.publishMu.
func (r *Output) projectWindow(ctx context.Context, proc *Process, cursor int64) safeProjection {
	chunks, next, truncated := proc.buffer.ReadSince(0)
	if len(chunks) == 0 {
		return safeProjection{Next: next, Truncated: truncated, Screened: true, Empty: true}
	}
	head := chunks[0].Cursor
	r.jobs.mu.Lock()
	projector := r.projector
	r.jobs.mu.Unlock()
	out := safeProjection{Head: head, Next: next, Truncated: truncated}
	if projector == nil {
		return out
	}
	memory, err := r.screenMemory()
	if err != nil {
		return out
	}
	rawBytes := int64(0)
	for _, chunk := range chunks {
		rawBytes += int64(len(chunk.Text))
	}
	rawCharge := rawBytes*2 + 1024
	if err = memory.ReserveBytes(ctx, rawCharge); err != nil {
		return out
	}
	defer memory.ReleaseBytes(rawCharge)
	raw := joinOutputChunks(chunks)
	if resume := withheldThrough(proc.withheld, head); resume > head {
		drop := min(int(resume-head), len(raw))
		raw = raw[drop:]
		out.Head = head + int64(drop)
	}
	text, mappings, release, err := r.screenWindow(ctx, proc, projector, raw)
	defer release()
	if err != nil {
		return out
	}
	copyCharge := int64(len(text))*2 + int64(len(chunks))*128
	if err = memory.ReserveBytes(ctx, copyCharge); err != nil {
		return out
	}
	defer memory.ReleaseBytes(copyCharge)
	spans := make([]captureprojection.Span, len(mappings))
	for i, span := range mappings {
		spans[i] = captureprojection.Span{Start: int(span.InputStart), End: int(span.InputEnd)}
	}
	proc.withheld = recordWithheld(proc.withheld, head, out.Head, spans)
	out.Text = text
	out.Chunks = projectedChunks(chunks, out.Head, cursor, raw, text, mappings)
	out.Screened = true
	return out
}

// withheldThrough returns the end of the range the head sits inside, if any.
func withheldThrough(ranges []cursorRange, head int64) int64 {
	resume := head
	for _, span := range ranges {
		if span.start < head && head < span.end && span.end > resume {
			resume = span.end
		}
	}
	return resume
}

// recordWithheld merges masked spans and prunes from the buffer head.
func recordWithheld(
	ranges []cursorRange, windowHead, spanBase int64, spans []captureprojection.Span,
) []cursorRange {
	out := ranges[:0]
	for _, span := range ranges {
		if span.end > windowHead {
			out = append(out, span)
		}
	}
	for _, span := range spans {
		out = append(out, cursorRange{start: spanBase + int64(span.Start), end: spanBase + int64(span.End)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start < out[j].start })
	merged := out[:0]
	for _, span := range out {
		if len(merged) > 0 && span.start <= merged[len(merged)-1].end {
			merged[len(merged)-1].end = max(merged[len(merged)-1].end, span.end)
		} else {
			merged = append(merged, span)
		}
	}
	return merged
}

func (r *Output) publishStream(ctx context.Context, proc *Process, stream string, cursor int64) {
	if r == nil || proc == nil {
		return
	}
	r.jobs.mu.Lock()
	silent := proc.silent
	running := proc.running
	r.jobs.mu.Unlock()
	if !silent && running {
		r.sweeper.onOutputChunk() //nolint:contextcheck // The sweeper loop outlives this call and holds its own context.
	}
	if r.publish == nil {
		return
	}
	if silent {
		// Screen before eviction to retain masked ranges that cross writes.
		if proc.buffer.Overflowing() {
			proc.publishMu.Lock()
			r.projectWindow(ctx, proc, 0)
			proc.publishMu.Unlock()
		}
		return
	}
	proc.publishMu.Lock()
	projection := r.projectWindow(ctx, proc, 0)
	if projection.Empty {
		proc.publishMu.Unlock()
		return
	}
	text := projection.Text
	if !projection.Screened {
		text = captureUnavailableText
	}
	reset := !strings.HasPrefix(text, proc.safePublished)
	if !reset {
		text = strings.TrimPrefix(text, proc.safePublished)
		proc.safePublished += text
	} else {
		proc.safePublished = text
	}
	next, truncated := projection.Next, projection.Truncated
	proc.publishMu.Unlock()
	if text == "" {
		return
	}
	r.publish(ctx, proc.ProjectID, proc.SessionID, api.BackgroundProcessEvent{
		ProcessID: proc.Handle,
		SessionID: proc.SessionID,
		Stream:    stream,
		Text:      text,
		EndOffset: next,
		Running:   running,
		Truncated: truncated,
		Reset:     reset,
	})
}

// tailScreening records whether projection produced a safe tail.
type tailScreening int

const (
	// tailScreened means the projection completed.
	tailScreened tailScreening = iota
	// tailFailed suppresses a tail whose projection failed.
	tailFailed
)

// safeOutput screens the whole retained window; callers cut their own tail
// from it so the tail and the body are one screening.
func (r *Output) safeOutput(ctx context.Context, proc *Process) (body string, evicted bool, screening tailScreening) {
	proc.publishMu.Lock()
	defer proc.publishMu.Unlock()
	projection := r.projectWindow(ctx, proc, 0)
	if !projection.Screened {
		return "", projection.Truncated, tailFailed
	}
	return RenderChunks(projection.Chunks), projection.Truncated, tailScreened
}

// screenedOrSuppressed withholds text whose screening was attempted and failed.
func screenedOrSuppressed(text string, screening tailScreening) string {
	if screening == tailFailed {
		return captureUnavailableText
	}
	return text
}

func processCaptureScope(proc *Process) captureprojection.Scope {
	return captureprojection.ScopeFor(proc.ProjectID, proc.RootSessionID, proc.SessionID)
}

// ProjectScreen derives terminal text and raster data from one screened grid.
func (r *Output) ProjectScreen(
	ctx context.Context, scope captureprojection.Scope, screen ScreenSnapshot,
) (ScreenSnapshot, error) {
	if r == nil {
		return ScreenSnapshot{}, captureprojection.ErrUnavailable
	}
	r.jobs.mu.Lock()
	projector := r.projector
	r.jobs.mu.Unlock()
	if projector == nil {
		return ScreenSnapshot{}, captureprojection.ErrUnavailable
	}
	lines, _, err := projector.Grid(ctx, scope, "capture.terminal", screen.Lines)
	if err != nil {
		return ScreenSnapshot{}, err
	}
	screen.Lines = lines
	return screen, nil
}

// ProjectCapturedText screens capture metadata such as artifact captions with
// the same scope as the captured bytes.
func (r *Output) ProjectCapturedText(
	ctx context.Context, projectID, rootSessionID, sessionID, label, value string,
) (string, error) {
	if r == nil {
		return "", captureprojection.ErrUnavailable
	}
	r.jobs.mu.Lock()
	projector := r.projector
	r.jobs.mu.Unlock()
	if projector == nil {
		return "", captureprojection.ErrUnavailable
	}
	projected, err := projector.Text(ctx, captureprojection.Scope{
		ProjectID: projectID, RootSessionID: rootSessionID, SessionID: sessionID,
	}, label, value)
	return projected.Value, err
}

func joinOutputChunks(chunks []OutputChunk) string {
	var out strings.Builder
	for _, chunk := range chunks {
		out.WriteString(chunk.Text)
	}
	return out.String()
}
