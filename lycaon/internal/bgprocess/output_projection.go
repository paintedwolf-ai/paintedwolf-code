package bgprocess

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/pkg/api"
)

// captureUnavailableText stands in for output that could not be screened.
const captureUnavailableText = "[Capture screening unavailable]"

// safeProjection is one screened view of a process's retained output window.
type safeProjection struct {
	Text string
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
func (r *Registry) projectWindow(ctx context.Context, proc *Process) safeProjection {
	chunks, next, truncated := proc.buffer.ReadSince(0)
	if len(chunks) == 0 {
		return safeProjection{Next: next, Truncated: truncated, Screened: true, Empty: true}
	}
	head := chunks[0].Cursor
	raw := joinOutputChunks(chunks)
	r.mu.Lock()
	projector := r.projector
	r.mu.Unlock()
	out := safeProjection{Head: head, Next: next, Truncated: truncated}
	if projector == nil {
		return out
	}
	if resume := withheldThrough(proc.withheld, head); resume > head {
		drop := min(int(resume-head), len(raw))
		raw = raw[drop:]
		out.Head = head + int64(drop)
	}
	projected, spans, err := projector.TextSpans(ctx, processCaptureScope(proc), "capture.process", raw)
	if err != nil {
		return out
	}
	proc.withheld = recordWithheld(proc.withheld, head, out.Head, spans)
	out.Text = projected.Value
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
	return out
}

func (r *Registry) publishStream(ctx context.Context, proc *Process, stream string, cursor int64) {
	if r == nil || r.publish == nil || proc == nil {
		return
	}
	r.mu.Lock()
	silent := proc.silent
	running := proc.running
	r.mu.Unlock()
	if silent {
		// Screen before eviction to retain masked ranges that cross writes.
		if proc.buffer.Overflowing() {
			proc.publishMu.Lock()
			r.projectWindow(ctx, proc)
			proc.publishMu.Unlock()
		}
		return
	}
	proc.publishMu.Lock()
	projection := r.projectWindow(ctx, proc)
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
	// tailUnscreened means no projector is configured.
	tailUnscreened tailScreening = iota
	// tailScreened means the projection completed.
	tailScreened
	// tailFailed suppresses a tail whose projection failed.
	tailFailed
)

// safeOutput screens the whole retained window; callers cut their own tail
// from it so the tail and the body are one screening.
func (r *Registry) safeOutput(ctx context.Context, proc *Process) (body string, evicted bool, screening tailScreening) {
	proc.publishMu.Lock()
	defer proc.publishMu.Unlock()
	chunks, _, evicted := proc.buffer.ReadSince(0)
	if len(chunks) == 0 {
		return "", evicted, tailScreened
	}
	r.mu.Lock()
	projector := r.projector
	r.mu.Unlock()
	body = RenderChunks(trimWithheldChunks(chunks, proc.withheld))
	if projector == nil {
		return body, evicted, tailUnscreened
	}
	projected, err := projector.Text(ctx, processCaptureScope(proc), "capture.process", body)
	if err != nil {
		return "", evicted, tailFailed
	}
	return projected.Value, evicted, tailScreened
}

// screenedOrSuppressed withholds text whose screening was attempted and failed.
func screenedOrSuppressed(text string, screening tailScreening) string {
	if screening == tailFailed {
		return captureUnavailableText
	}
	return text
}

// trimWithheldChunks drops leading bytes that fall inside a withheld range.
func trimWithheldChunks(chunks []OutputChunk, withheld []cursorRange) []OutputChunk {
	if len(chunks) == 0 {
		return chunks
	}
	resume := withheldThrough(withheld, chunks[0].Cursor)
	if resume <= chunks[0].Cursor {
		return chunks
	}
	out := make([]OutputChunk, 0, len(chunks))
	for _, chunk := range chunks {
		end := chunk.Cursor + int64(len(chunk.Text))
		if end <= resume {
			continue
		}
		if chunk.Cursor < resume {
			drop := int(resume - chunk.Cursor)
			chunk.Cursor = resume
			chunk.Text = chunk.Text[drop:]
		}
		out = append(out, chunk)
	}
	return out
}

func processCaptureScope(proc *Process) captureprojection.Scope {
	return captureprojection.ScopeFor(proc.ProjectID, proc.RootSessionID, proc.SessionID)
}

// ProjectScreen derives terminal text and raster data from one screened grid.
func (r *Registry) ProjectScreen(
	ctx context.Context, scope captureprojection.Scope, screen ScreenSnapshot,
) (ScreenSnapshot, error) {
	if r == nil {
		return ScreenSnapshot{}, captureprojection.ErrUnavailable
	}
	r.mu.Lock()
	projector := r.projector
	r.mu.Unlock()
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
func (r *Registry) ProjectCapturedText(
	ctx context.Context, projectID, rootSessionID, sessionID, label, value string,
) (string, error) {
	if r == nil {
		return "", captureprojection.ErrUnavailable
	}
	r.mu.Lock()
	projector := r.projector
	r.mu.Unlock()
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

func toAPIChunks(chunks []OutputChunk) []api.BackgroundProcessChunk {
	if len(chunks) == 0 {
		return nil
	}
	out := make([]api.BackgroundProcessChunk, len(chunks))
	for i, c := range chunks {
		out[i] = api.BackgroundProcessChunk{
			Offset: c.Cursor,
			Stream: c.Stream,
			Text:   c.Text,
		}
	}
	return out
}
