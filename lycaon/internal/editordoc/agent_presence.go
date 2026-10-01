package editordoc

import (
	"context"
	"errors"
	"math"
	"unicode/utf16"

	"github.com/lycaon/lycaon/internal/documentcore"
)

// maxHeldSpanUnits bounds the text kept to check that a span still reads as the agent saw it.
const maxHeldSpanUnits = 64 << 10

// TextSpan addresses part of one document text. Lines are 1-based and
// inclusive. Characters are UTF-16 offsets within their line; without them the
// span covers whole lines. Equal start and end positions address an insertion point.
type TextSpan struct {
	StartLine, EndLine           int
	StartCharacter, EndCharacter *int
}

// AnchoredSpan is a span as CRDT relative positions in its document epoch.
type AnchoredSpan struct {
	Anchor, Head []byte
	// Expected is the span text, kept only when Checkable.
	Expected  string
	Checkable bool
}

// AnchoredText is a span set anchored in one document state.
type AnchoredText struct {
	DocumentID      string
	Epoch, Revision int64
	Spans           []AnchoredSpan
}

// AnchorSpans anchors spans of the text at revision, or the current head when
// revision is zero. A revision from an earlier epoch cannot anchor and returns
// ErrRevisionConflict.
func (s *Service) AnchorSpans(ctx context.Context, projectID, documentID string, revision int64, spans []TextSpan) (*AnchoredText, error) {
	if s == nil || s.store == nil {
		return nil, ErrNotFound
	}
	s.ops.RLock()
	defer s.ops.RUnlock()
	unlock := s.lockDocuments([]string{documentID})
	defer unlock()
	current, err := s.checked(ctx, documentID, projectID)
	if err != nil {
		return nil, err
	}
	return s.anchorSpans(ctx, current, revision, spans)
}

// AnchorPathSpans uses the existing document head without opening a document.
// ok is false when the path has no document.
func (s *Service) AnchorPathSpans(ctx context.Context, projectID, rootID, path string, spans []TextSpan) (*AnchoredText, bool, error) {
	if s == nil || s.store == nil || s.roots == nil {
		return nil, false, nil
	}
	p, err := s.roots.Get(ctx, projectID)
	if err != nil {
		return nil, false, err
	}
	d, err := s.store.GetByIdentity(ctx, p.ID, p.BranchForRoot(rootID), rootID, path)
	if errors.Is(err, ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	out, err := s.AnchorSpans(ctx, projectID, d.ID, 0, spans)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

// SpansHold reports, per span, whether the current head still holds the text
// it was anchored with. Spans that were not checkable report false.
func (s *Service) SpansHold(ctx context.Context, projectID, documentID string, spans []AnchoredSpan) ([]bool, error) {
	if s == nil || s.store == nil {
		return nil, ErrNotFound
	}
	s.ops.RLock()
	defer s.ops.RUnlock()
	unlock := s.lockDocuments([]string{documentID})
	defer unlock()
	current, err := s.checked(ctx, documentID, projectID)
	if err != nil {
		return nil, err
	}
	s.replicas.mu.Lock()
	defer s.replicas.mu.Unlock()
	entry, err := s.loadReplica(ctx, current)
	if err != nil {
		return nil, err
	}
	held := make([]bool, len(spans))
	for i, span := range spans {
		if !span.Checkable {
			continue
		}
		_, err := s.replicas.engine.Call(ctx, documentcore.Request{Action: "resolve", OmitText: true, Handle: entry.handle,
			Anchors: []documentcore.AnchoredEdit{{Start: span.Anchor, End: span.Head, Expected: span.Expected}}})
		var rejection *documentcore.Rejected
		switch {
		case err == nil:
			held[i] = true
		case errors.As(err, &rejection) && (rejection.Code == "anchor_conflict" || rejection.Code == "invalid_anchor"):
		default:
			return nil, err
		}
	}
	return held, nil
}

func (s *Service) anchorSpans(ctx context.Context, current *Document, revision int64, spans []TextSpan) (*AnchoredText, error) {
	s.replicas.mu.Lock()
	defer s.replicas.mu.Unlock()
	entry, err := s.loadReplica(ctx, current)
	if err != nil {
		return nil, err
	}
	base := current
	handle := entry.handle
	if revision != 0 && revision != current.Revision {
		base, err = s.pinnedDocument(ctx, current, revision)
		if err != nil {
			return nil, err
		}
		if base.Epoch != entry.head.Epoch {
			return nil, ErrRevisionConflict
		}
		s.replicas.next++
		handle = s.replicas.next
		if _, err := s.replicas.engine.Call(ctx, documentcore.Request{Action: "open", OmitText: true, Handle: handle, Client: entry.head.HostClient, Update: base.CRDTUpdate}); err != nil {
			return nil, err
		}
		defer func() { _, _ = s.replicas.engine.Call(ctx, documentcore.Request{Action: "drop", Handle: handle}) }()
	}
	units := utf16.Encode([]rune(base.Draft))
	if len(units) > math.MaxUint32 {
		return nil, errors.New("document too large to anchor")
	}
	lines := lineStarts(units)
	edits := make([]documentcore.Edit, 0, len(spans))
	for _, span := range spans {
		start, end, ok := spanOffsets(units, lines, span)
		if !ok {
			return nil, ErrRevisionConflict
		}
		edits = append(edits, documentcore.Edit{Index: start, Delete: end - start})
	}
	out := &AnchoredText{DocumentID: current.ID, Epoch: entry.head.Epoch, Revision: base.Revision}
	if len(edits) == 0 {
		return out, nil
	}
	anchored, err := s.replicas.engine.Call(ctx, documentcore.Request{Action: "anchor", OmitText: true, Handle: handle, Edits: edits})
	if err != nil {
		return nil, err
	}
	if len(anchored.Anchors) != len(edits) {
		return nil, errors.New("document core anchored an unexpected span count")
	}
	out.Spans = make([]AnchoredSpan, len(edits))
	for i, a := range anchored.Anchors {
		checkable := edits[i].Delete <= maxHeldSpanUnits
		out.Spans[i] = AnchoredSpan{Anchor: a.Start, Head: a.End, Checkable: checkable}
		if checkable {
			out.Spans[i].Expected = a.Expected
		}
	}
	return out, nil
}

// lineStarts returns the UTF-16 offset of each line's first unit.
func lineStarts(units []uint16) []uint32 {
	starts := []uint32{0}
	for i, u := range units {
		if u == '\n' {
			starts = append(starts, uint32(i+1))
		}
	}
	return starts
}

// spanOffsets converts a span to UTF-16 offsets. Whole-line spans exclude the
// final line break so an edit that appends a line does not change the span.
func spanOffsets(units []uint16, lines []uint32, span TextSpan) (uint32, uint32, bool) {
	if span.StartLine < 1 || span.EndLine < span.StartLine || span.EndLine > len(lines) {
		return 0, 0, false
	}
	lineStart := func(line int) int { return int(lines[line-1]) }
	lineEnd := func(line int) int {
		if line < len(lines) {
			return int(lines[line]) - 1
		}
		return len(units)
	}
	start, end := lineStart(span.StartLine), lineEnd(span.EndLine)
	if c := span.StartCharacter; c != nil {
		if *c < 0 || *c > lineEnd(span.StartLine)-start {
			return 0, 0, false
		}
		start += *c
	}
	if c := span.EndCharacter; c != nil {
		if *c < 0 || *c > lineEnd(span.EndLine)-lineStart(span.EndLine) {
			return 0, 0, false
		}
		end = lineStart(span.EndLine) + *c
	}
	if end < start {
		return 0, 0, false
	}
	// Both offsets lie within the text, whose length the line table already holds as uint32.
	return uint32(start), uint32(end), true // #nosec G115 -- 0 <= start <= end <= len(units)
}

// PathText returns the current draft of the existing document for a
// project-tree path without opening one. ok is false when there is none.
func (s *Service) PathText(ctx context.Context, projectID, rootID, path string) (string, bool, error) {
	if s == nil || s.store == nil || s.roots == nil {
		return "", false, nil
	}
	p, err := s.roots.Get(ctx, projectID)
	if err != nil {
		return "", false, err
	}
	d, err := s.store.GetByIdentity(ctx, p.ID, p.BranchForRoot(rootID), rootID, path)
	if errors.Is(err, ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return d.Draft, true, nil
}
