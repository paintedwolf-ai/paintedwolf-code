package eventing

import (
	"context"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/agentpresence"
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
