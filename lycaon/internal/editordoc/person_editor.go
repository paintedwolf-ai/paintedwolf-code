package editordoc

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/sourcebranch"
)

// OpenDocument is one document a person has open in the editor.
type OpenDocument struct {
	RootID, Path string
	Unsaved      bool
	// View is the person's most recently moved live editor of the document; nil when none shows it.
	View *EditorView
}

// EditorView is the main selection of one live editor. An empty selection is a cursor.
type EditorView struct {
	StartLine, EndLine int
	Empty              bool
	SelectedAt         time.Time
}

// PersonEditor lists what personID has open on rootIDs of a project branch:
// documents with a live view first, most recently moved first, then root order and path.
func (s *Service) PersonEditor(ctx context.Context, projectID string, branch sourcebranch.ID, personID string, rootIDs []string) ([]OpenDocument, error) {
	personID = strings.TrimSpace(personID)
	if s == nil || s.store == nil || personID == "" || len(rootIDs) == 0 {
		return nil, nil
	}
	p, err := s.projectBranch(ctx, strings.TrimSpace(projectID), branch)
	if err != nil {
		return nil, err
	}
	documents, err := s.store.queryDocuments(ctx, `SELECT `+documentColumns+` FROM editor_documents
		WHERE project_id=? AND id IN (SELECT document_id FROM editor_document_retention WHERE person_id=?) ORDER BY path,id`, p.ID, personID)
	if err != nil {
		return nil, err
	}
	rootOrder := func(rootID string) int { return slices.Index(rootIDs, rootID) }
	out := make([]OpenDocument, 0, len(documents))
	for _, d := range documents {
		if rootOrder(d.RootID) < 0 || d.BranchID != p.BranchForRoot(d.RootID) {
			continue
		}
		view, err := s.personView(ctx, d, personID)
		if err != nil {
			return nil, err
		}
		out = append(out, OpenDocument{RootID: d.RootID, Path: d.Path, Unsaved: d.Dirty, View: view})
	}
	slices.SortStableFunc(out, func(a, b OpenDocument) int {
		switch {
		case a.View != nil && b.View != nil:
			return b.View.SelectedAt.Compare(a.View.SelectedAt)
		case a.View != nil:
			return -1
		case b.View != nil:
			return 1
		}
		return rootOrder(a.RootID) - rootOrder(b.RootID)
	})
	return out, nil
}

// personView places the person's most recently moved selection in d.
// A selection whose anchors no longer place leaves the document without a view.
func (p *PresenceService) personView(ctx context.Context, d *Document, personID string) (*EditorView, error) {
	s := p.s
	var newest *Participant
	s.presenceMu.Lock()
	for _, participant := range s.participants[d.ID] {
		if participant.PersonID != personID || len(participant.Ranges) == 0 {
			continue
		}
		if newest == nil || participant.SelectedAt.After(newest.SelectedAt) {
			chosen := participant
			chosen.Ranges = clonePresenceRanges(participant.Ranges)
			newest = &chosen
		}
	}
	s.presenceMu.Unlock()
	if newest == nil {
		return nil, nil
	}
	main := newest.Ranges[newest.Main]
	start, end, text, err := s.locateSelection(ctx, d, main)
	var rejection *documentcore.Rejected
	if errors.As(err, &rejection) && (rejection.Code == "anchor_conflict" || rejection.Code == "invalid_anchor") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	view := &EditorView{StartLine: utf16Line(text, start), EndLine: utf16Line(text, end), Empty: start == end, SelectedAt: newest.SelectedAt}
	if !view.Empty && view.EndLine > view.StartLine && endsAtLineStart(text, end) {
		view.EndLine--
	}
	return view, nil
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
	units := uint32(0)
	previous := rune(0)
	for _, r := range text {
		if units >= offset {
			break
		}
		previous = r
		units += uint32(utf16.RuneLen(r))
	}
	return previous == '\n'
}
