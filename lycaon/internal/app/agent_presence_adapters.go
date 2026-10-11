package app

import (
	"context"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/editordoc"
)

// presenceAnchors anchors presence spans in editor documents.
type presenceAnchors struct {
	service *editordoc.Service
}

func (a presenceAnchors) AnchorSpans(ctx context.Context, projectID, documentID string, revision int64, spans []agentpresence.Span) (agentpresence.Anchored, error) {
	out, err := a.service.AnchorSpans(ctx, projectID, documentID, revision, textSpans(spans))
	if err != nil {
		return agentpresence.Anchored{}, err
	}
	return anchored(out), nil
}

func (a presenceAnchors) AnchorPathSpans(ctx context.Context, projectID string, target agentpresence.Target, spans []agentpresence.Span) (agentpresence.Anchored, bool, error) {
	out, ok, err := a.service.AnchorPathSpans(ctx, projectID, target.RootID, target.Path, textSpans(spans))
	if err != nil || !ok {
		return agentpresence.Anchored{}, ok, err
	}
	return anchored(out), true, nil
}

func (a presenceAnchors) SpansHold(ctx context.Context, projectID, documentID string, spans []agentpresence.AnchoredSpan) ([]bool, error) {
	in := make([]editordoc.AnchoredSpan, len(spans))
	for i, span := range spans {
		in[i] = editordoc.AnchoredSpan{Anchor: span.Anchor, Head: span.Head, Expected: span.Expected, Checkable: span.Checkable}
	}
	return a.service.SpansHold(ctx, projectID, documentID, in)
}

func textSpans(spans []agentpresence.Span) []editordoc.TextSpan {
	out := make([]editordoc.TextSpan, len(spans))
	for i, span := range spans {
		out[i] = editordoc.TextSpan{StartLine: span.StartLine, EndLine: span.EndLine, StartCharacter: span.StartCharacter, EndCharacter: span.EndCharacter}
	}
	return out
}

func anchored(in *editordoc.AnchoredText) agentpresence.Anchored {
	out := agentpresence.Anchored{DocumentID: in.DocumentID, Epoch: in.Epoch, Revision: in.Revision, Spans: make([]agentpresence.AnchoredSpan, len(in.Spans))}
	for i, span := range in.Spans {
		out.Spans[i] = agentpresence.AnchoredSpan{Anchor: span.Anchor, Head: span.Head, Expected: span.Expected, Checkable: span.Checkable}
	}
	return out
}
