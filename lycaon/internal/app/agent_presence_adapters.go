package app

import (
	"context"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/pkg/api"
)

const presenceChatCacheSize = 1024

// presenceChats places sessions in their chats. A session's project, chat, and
// job never change, so they are cached; titles arrive through session events.
type presenceChats struct {
	store      session.Store
	workerJobs func(ctx context.Context, childSessionID string) (*api.WorkerTask, bool)

	mu    sync.Mutex
	cache map[string]agentpresence.ChatRef
}

func (c *presenceChats) Chat(ctx context.Context, sessionID string) (agentpresence.ChatRef, bool) {
	sessionID = strings.TrimSpace(sessionID)
	c.mu.Lock()
	ref, ok := c.cache[sessionID]
	c.mu.Unlock()
	if ok {
		return ref, true
	}
	if c.store == nil {
		return agentpresence.ChatRef{}, false
	}
	sess, err := c.store.Get(ctx, sessionID)
	if err != nil || sess == nil || strings.TrimSpace(sess.ProjectID) == "" {
		return agentpresence.ChatRef{}, false
	}
	ref = agentpresence.ChatRef{ProjectID: sess.ProjectID, SessionID: sess.ID, Title: sess.Title}
	if strings.TrimSpace(sess.ParentSessionID) != "" {
		root := session.RootSessionID(ctx, c.store, sessionID)
		chat, err := c.store.Get(ctx, root)
		if err != nil || chat == nil {
			return agentpresence.ChatRef{}, false
		}
		ref.SessionID, ref.Title, ref.JobID = chat.ID, chat.Title, sessionID
		if c.workerJobs != nil {
			if task, found := c.workerJobs(ctx, sessionID); found && task != nil && task.ID != "" {
				ref.JobID = task.ID
			}
		}
	}
	c.mu.Lock()
	if c.cache == nil || len(c.cache) >= presenceChatCacheSize {
		c.cache = make(map[string]agentpresence.ChatRef)
	}
	cached := ref
	cached.Title = ""
	c.cache[sessionID] = cached
	c.mu.Unlock()
	return ref, true
}

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
