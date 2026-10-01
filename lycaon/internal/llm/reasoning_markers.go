package llm

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
)

// reasoningMarkerProvider separates reasoning a serving stack left in the
// content channel from visible text, using the markers the model family's
// thinking rule declares. It sits inside the lifecycle so every transport's
// output is read the same way and first-output timing sees the separated
// text.
type reasoningMarkerProvider struct{ modelcall.Provider }

func (p *reasoningMarkerProvider) markers(req modelcall.CompletionRequest) *modelinfo.ReasoningMarkers {
	model := req.Model
	if model == "" && len(p.Models()) > 0 {
		model = p.Models()[0].ID
	}
	rule, ok := modelinfo.MatchThinkingRule(model)
	if !ok {
		return nil
	}
	return rule.ReasoningMarkers
}

func (p *reasoningMarkerProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	out, err := p.Provider.Complete(ctx, req)
	if markers := p.markers(req); markers != nil {
		separateCompletionReasoning(out, *markers)
	}
	return out, err
}

func (p *reasoningMarkerProvider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch, err := p.Provider.Stream(ctx, req)
	if err != nil || ch == nil {
		return ch, err
	}
	markers := p.markers(req)
	if markers == nil {
		return ch, nil
	}
	out := make(chan modelcall.StreamChunk, 64)
	go func() {
		defer close(out)
		reader := newReasoningMarkerReader(*markers)
		for chunk := range ch {
			mapped, keep := reader.chunk(chunk)
			if !keep {
				continue
			}
			if !modelcall.SendChunk(ctx, out, mapped) {
				modelcall.DrainStream(ch)
				return
			}
		}
	}()
	return out, nil
}

// separateCompletionReasoning applies the markers to a complete response. A
// separately reported reasoning field means the family's open tag stood alone
// in the content and the text after it is the visible answer.
func separateCompletionReasoning(c *modelcall.Completion, markers modelinfo.ReasoningMarkers) {
	if c == nil || c.Content == "" {
		return
	}
	reader := newReasoningMarkerReader(markers)
	reader.sideChannel = c.Reasoning != ""
	fed := reader.feed(c.Content)
	rest := reader.flush()
	c.Content = fed.visible + rest.visible
	if inline := fed.reasoning + rest.reasoning; inline != "" {
		c.Reasoning = inline + c.Reasoning
	}
}

// markerState is where the reader stands in the content channel.
type markerState uint8

const (
	// markerLead precedes visible text: whitespace and the open tag may lead.
	markerLead markerState = iota
	// markerInline follows the open tag: content is reasoning until the close
	// tag, or until reasoning arrives on its own channel.
	markerInline
	// markerAfterClose follows the reasoning: whitespace and a stray close tag
	// may lead the visible text.
	markerAfterClose
	// markerVisible passes content through.
	markerVisible
)

// separated is one feed's classification of content.
type separated struct {
	visible   string
	reasoning string
}

// reasoningMarkerReader classifies content bytes as they arrive. Bytes that
// could still begin a tag stay pending until the next feed settles them.
type reasoningMarkerReader struct {
	open, close string
	state       markerState
	pending     string
	// sideChannel records that reasoning arrived in its own field, so an open
	// tag in the content stood alone and nothing after it is reasoning.
	sideChannel bool
	// inlineFresh drops the whitespace a template writes after the open tag.
	inlineFresh bool
}

func newReasoningMarkerReader(markers modelinfo.ReasoningMarkers) *reasoningMarkerReader {
	return &reasoningMarkerReader{open: markers.Open, close: markers.Close}
}

// chunk applies the reader to one stream chunk. It reports false when the
// chunk carried only content that the reader is still holding.
func (r *reasoningMarkerReader) chunk(c modelcall.StreamChunk) (modelcall.StreamChunk, bool) {
	if c.ResetReasoning {
		r.state, r.pending, r.sideChannel, r.inlineFresh = markerLead, "", false, false
	}
	var inline string
	if c.Reasoning != "" {
		inline += r.reasoningArrived()
	}
	hadContent := c.Content != ""
	var visible string
	if hadContent {
		fed := r.feed(c.Content)
		visible, inline = fed.visible, inline+fed.reasoning
	}
	if c.Done || c.Err != nil {
		rest := r.flush()
		visible, inline = visible+rest.visible, inline+rest.reasoning
	}
	c.Content = visible
	if inline != "" {
		c.Reasoning = inline + c.Reasoning
	}
	if hadContent && c.Content == "" && c.Reasoning == "" && streamChunkCarriesOnlyContent(c) {
		return c, false
	}
	return c, true
}

func streamChunkCarriesOnlyContent(c modelcall.StreamChunk) bool {
	return !c.Done && c.Err == nil && len(c.ToolCalls) == 0 && len(c.ReasoningDetails) == 0 &&
		!c.Usage.Reported() && c.ProviderID == "" && !c.Fallback && !c.Scripted && !c.ResetReasoning
}

// feed classifies text in content order.
func (r *reasoningMarkerReader) feed(text string) separated {
	var out separated
	r.pending += text
	for r.pending != "" {
		switch r.state {
		case markerLead, markerAfterClose:
			tag := r.open
			if r.state == markerAfterClose {
				tag = r.close
			}
			rest := strings.TrimLeft(r.pending, " \t\r\n")
			if rest == "" {
				return out
			}
			if strings.HasPrefix(rest, tag) {
				r.pending = rest[len(tag):]
				if r.state == markerLead {
					r.enterInline()
				}
				continue
			}
			if strings.HasPrefix(tag, rest) {
				return out
			}
			if r.state == markerLead {
				out.visible += r.pending
			} else {
				out.visible += rest
			}
			r.pending = ""
			r.state = markerVisible
		case markerInline:
			if r.inlineFresh {
				r.pending = strings.TrimLeft(r.pending, " \t\r\n")
				if r.pending == "" {
					return out
				}
				r.inlineFresh = false
			}
			if i := strings.Index(r.pending, r.close); i >= 0 {
				out.reasoning += strings.ReplaceAll(r.pending[:i], r.open, "")
				r.pending = r.pending[i+len(r.close):]
				r.state = markerAfterClose
				continue
			}
			keep := partialTagSuffix(r.pending, r.close, r.open)
			settled := r.pending[:len(r.pending)-keep]
			out.reasoning += strings.ReplaceAll(settled, r.open, "")
			r.pending = r.pending[len(settled):]
			return out
		case markerVisible:
			out.visible += r.pending
			r.pending = ""
		}
	}
	return out
}

// enterInline starts the reasoning block the open tag announced. Reasoning
// that already arrived on its own channel means the tag stood alone.
func (r *reasoningMarkerReader) enterInline() {
	if r.sideChannel {
		r.state = markerAfterClose
		return
	}
	r.state = markerInline
	r.inlineFresh = true
}

// reasoningArrived notes reasoning on its own channel and returns any inline
// bytes that were still pending, which belong to the reasoning.
func (r *reasoningMarkerReader) reasoningArrived() string {
	r.sideChannel = true
	if r.state != markerInline {
		return ""
	}
	inline := strings.ReplaceAll(r.pending, r.open, "")
	r.pending = ""
	r.state = markerAfterClose
	return inline
}

// flush settles what is pending when the stream ends: a tag that never
// completed was visible text, and an open block that never closed was
// reasoning.
func (r *reasoningMarkerReader) flush() separated {
	var out separated
	switch r.state {
	case markerLead:
		if strings.TrimSpace(r.pending) != "" {
			out.visible = r.pending
		}
	case markerAfterClose:
		out.visible = strings.TrimLeft(r.pending, " \t\r\n")
	case markerInline:
		out.reasoning = strings.ReplaceAll(r.pending, r.open, "")
	case markerVisible:
		out.visible = r.pending
	}
	r.pending = ""
	r.state = markerVisible
	return out
}

// partialTagSuffix reports how many trailing bytes of s could still begin
// one of the tags, so they are held rather than classified.
func partialTagSuffix(s string, tags ...string) int {
	longest := 0
	for _, tag := range tags {
		for k := min(len(tag)-1, len(s)); k > longest; k-- {
			if strings.HasSuffix(s, tag[:k]) {
				longest = k
				break
			}
		}
	}
	return longest
}
