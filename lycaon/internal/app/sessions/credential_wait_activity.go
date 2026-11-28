package sessions

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// credentialWaitShown is how long a session waits before the wait is shown,
// and the shortest interval between progress refreshes.
const credentialWaitShown = time.Second

type activityPublisher interface {
	PublishActivity(ctx context.Context, projectIDOrDir, sessionID string, ev api.ActivityEvent)
}

// credentialWaitActivity is one session's indexing_credentials lease on one root.
type credentialWaitActivity struct {
	index *credentialIndex
	wait  tools.CredentialWait
	root  *credentialRoot
	event *api.ActivityEvent
}

func (i *credentialIndex) waitActivity(wait tools.CredentialWait, root *credentialRoot) *credentialWaitActivity {
	if i.events == nil || wait.SessionID == "" {
		return nil
	}
	return &credentialWaitActivity{index: i, wait: wait, root: root}
}

// show opens the lease, or refreshes it when the counts moved.
func (a *credentialWaitActivity) show(ctx context.Context) {
	progress := a.index.progress(a.root)
	if a.event == nil {
		a.event = &api.ActivityEvent{
			ActivityID: uuid.NewString(),
			SessionID:  a.wait.SessionID,
			Kind:       api.ActivityKindIndexingCredentials,
			Status:     api.ActivityStatusActive,
			StartedAt:  a.index.now().UTC(),
		}
	} else if *a.event.Indexing == progress {
		return
	}
	a.event.Indexing = &progress
	a.index.events.PublishActivity(ctx, a.wait.ProjectID, a.wait.SessionID, *a.event)
}

// finish closes a shown lease, including after the waiter is cancelled.
func (a *credentialWaitActivity) finish(ctx context.Context) {
	if a == nil || a.event == nil {
		return
	}
	a.event.Status = api.ActivityStatusDone
	a.index.events.PublishActivity(context.WithoutCancel(ctx), a.wait.ProjectID, a.wait.SessionID, *a.event)
}

func (i *credentialIndex) progress(root *credentialRoot) api.CredentialIndexingProgress {
	i.mu.Lock()
	label := root.label
	i.mu.Unlock()
	return api.CredentialIndexingProgress{
		RootID:              root.id,
		RootLabel:           label,
		DirectoriesVisited:  int(root.directories.Load()),
		CredentialFilesRead: int(root.credentialFiles.Load()),
	}
}
