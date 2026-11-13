package editordoc

import (
	"context"
	"errors"
	"strings"
	"unicode/utf16"

	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/sourcebranch"
)

// EditorShown is a document a window's visible editor shows.
type EditorShown struct {
	ClientID, DocumentID string
}

// VisibleDocument is a shown document with that window's selection.
type VisibleDocument struct {
	RootID, Path string
	Unsaved      bool
	// Selection is nil until the window's editor reports one.
	Selection *LineSelection
}

// LineSelection is an editor's main selection in 1-based lines. An empty selection is a cursor.
type LineSelection struct {
	StartLine, EndLine int
	Empty              bool
}

// VisibleDocuments resolves shown documents of a project branch in order.
// Documents on another branch, or no longer accepted, are left out.
func (s *Service) VisibleDocuments(ctx context.Context, projectID string, branch sourcebranch.ID, personID string, shown []EditorShown) ([]VisibleDocument, error) {
	if s == nil || s.store == nil || len(shown) == 0 {
		return nil, nil
	}
	p, err := s.projectBranch(ctx, strings.TrimSpace(projectID), branch)
	if err != nil {
		return nil, err
	}
	out := make([]VisibleDocument, 0, len(shown))
	for _, editor := range shown {
		d, err := s.checked(ctx, editor.DocumentID, p.ID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if d.BranchID != p.BranchForRoot(d.RootID) {
			continue
		}
		selection, err := s.windowSelection(ctx, d, editor.ClientID, personID)
		if err != nil {
			return nil, err
		}
		out = append(out, VisibleDocument{RootID: d.RootID, Path: d.Path, Unsaved: d.Dirty, Selection: selection})
	}
	return out, nil
}

// windowSelection places the window's main selection in d. A selection whose
// anchors no longer place leaves the document without one.
func (p *PresenceService) windowSelection(ctx context.Context, d *Document, clientID, personID string) (*LineSelection, error) {
	s := p.s
	s.presenceMu.Lock()
	participant, ok := s.participants[d.ID][clientID]
	var main PresenceRange
	if ok && participant.PersonID == personID && len(participant.Ranges) > 0 {
		main = participant.Ranges[participant.Main]
		main = PresenceRange{Anchor: append([]byte(nil), main.Anchor...), Head: append([]byte(nil), main.Head...)}
	}
	s.presenceMu.Unlock()
	if main.Anchor == nil {
		return nil, nil
	}
	start, end, text, err := s.locateSelection(ctx, d, main)
	var rejection *documentcore.Rejected
	if errors.As(err, &rejection) && (rejection.Code == "anchor_conflict" || rejection.Code == "invalid_anchor") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	selection := &LineSelection{StartLine: utf16Line(text, start), EndLine: utf16Line(text, end), Empty: start == end}
	if !selection.Empty && selection.EndLine > selection.StartLine && endsAtLineStart(text, end) {
		selection.EndLine--
	}
	return selection, nil
}

func (s *Service) locateSelection(ctx context.Context, d *Document, selection PresenceRange) (start, end uint32, text string, err error) {
	s.ops.RLock()
	defer s.ops.RUnlock()
	unlock := s.lockDocuments([]string{d.ID})
	defer unlock()
	current, err := s.checked(ctx, d.ID, d.ProjectID)
	if err != nil {
		return 0, 0, "", err
	}
	s.replicas.mu.Lock()
	defer s.replicas.mu.Unlock()
	entry, err := s.loadReplica(ctx, current)
	if err != nil {
		return 0, 0, "", err
	}
	located, err := s.replicas.engine.Call(ctx, documentcore.Request{Action: "locate", OmitText: true, Handle: entry.handle,
		Anchors: []documentcore.AnchoredEdit{{Start: selection.Anchor, End: selection.Head}}})
	if err != nil {
		return 0, 0, "", err
	}
	if len(located.Resolved) != 1 {
		return 0, 0, "", errors.New("document core located an unexpected range count")
	}
	placed := located.Resolved[0]
	return placed.Index, placed.Index + placed.Delete, current.Draft, nil
}

// utf16Line is the 1-based line holding a UTF-16 offset of text.
func utf16Line(text string, offset uint32) int {
	line, units := 1, uint32(0)
	for _, r := range text {
		if units >= offset {
			break
		}
		if r == '\n' {
			line++
		}
		units += uint32(utf16.RuneLen(r))
	}
	return line
}

func endsAtLineStart(text string, offset uint32) bool {
	units, previous := uint32(0), rune(0)
	for _, r := range text {
		if units >= offset {
			break
		}
		previous = r
		units += uint32(utf16.RuneLen(r))
	}
	return previous == '\n'
}
