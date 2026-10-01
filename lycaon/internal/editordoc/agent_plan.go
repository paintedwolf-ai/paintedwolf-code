package editordoc

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/documentcore"
)

type agentPlan struct {
	content string
	edits   []documentcore.Edit
}

func (s *Service) planAgentEdit(ctx context.Context, current *Document, in AgentEdit) (*agentPlan, error) {
	s.replicas.mu.Lock()
	defer s.replicas.mu.Unlock()
	entry, err := s.loadReplica(ctx, current)
	if err != nil {
		return nil, err
	}
	var base *Document
	if in.ExpectedRevision != current.Revision {
		base, err = s.pinnedDocument(ctx, current, in.ExpectedRevision)
		if err != nil {
			return nil, err
		}
		if base.Epoch != entry.head.Epoch {
			return nil, ErrRevisionConflict
		}
	} else {
		snapshot, err := s.replicas.engine.Call(ctx, documentcore.Request{Action: "inspect", OmitText: true, Handle: entry.handle, Checkpoint: true})
		if err != nil {
			return nil, err
		}
		copy := *current
		copy.CRDTUpdate = snapshot.Checkpoint
		base = &copy
	}
	s.replicas.next++
	handle := s.replicas.next
	_, err = s.replicas.engine.Call(ctx, documentcore.Request{Action: "open", OmitText: true, Handle: handle, Client: entry.head.HostClient, Update: base.CRDTUpdate})
	if err != nil {
		return nil, err
	}
	defer func() { _, _ = s.replicas.engine.Call(ctx, documentcore.Request{Action: "drop", Handle: handle}) }()
	content := strings.ReplaceAll(in.Content, "\r\n", "\n")
	// Guards are the whole lines the rewrite touched; the character edits stay
	// inside them, so an inserted line anchors at its line boundary.
	guards, textEdits, err := documentcore.HunkEdits(base.Draft, content)
	if err != nil {
		return nil, err
	}
	edits := append([]documentcore.Edit{}, guards...)
	edits = append(edits, textEdits...)
	anchored, err := s.replicas.engine.Call(ctx, documentcore.Request{Action: "anchor", OmitText: true, Handle: handle, Edits: edits})
	if err != nil {
		return nil, err
	}
	resolved, err := s.replicas.engine.Call(ctx, documentcore.Request{Action: "resolve", OmitText: true, Handle: entry.handle, Guards: anchored.Anchors[:len(guards)], Anchors: anchored.Anchors[len(guards):]})
	if err != nil {
		var rejection *documentcore.Rejected
		if errors.As(err, &rejection) && rejection.Code == "anchor_conflict" {
			return nil, ErrRevisionConflict
		}
		return nil, err
	}
	content, err = documentcore.ApplyEdits(current.Draft, resolved.Resolved)
	if err != nil {
		return nil, err
	}
	return &agentPlan{content: content, edits: resolved.Resolved}, nil
}
