package eventing

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/hostpower"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/pkg/api"
	"log/slog"
	"strings"
)

type ResourceLifetime interface {
	Track(string, int, func(context.Context) error)
}
type ProjectSessions interface {
	ListProjectSessions(context.Context, store.SummaryQuery) (api.SessionListPage, error)
}
type WorkerJobs interface {
	GetLatestByChildSessionID(context.Context, string) (*api.WorkerTask, bool)
}
type Runtime struct {
	Hub       events.ReplayHub
	Outbox    *eventoutbox.Outbox
	Presence  *events.Presence
	Publisher *events.Publisher
	Agents    *agentpresence.Tracker
	workers   WorkerJobs
}

func (b *Runtime) BindWorkers(jobs WorkerJobs) { b.workers = jobs }
func (b *Runtime) latestWorker(ctx context.Context, child string) (*api.WorkerTask, bool) {
	if b.workers == nil {
		return nil, false
	}
	return b.workers.GetLatestByChildSessionID(ctx, child)
}
func Build(ctx context.Context, database *db.Store, sessions *store.SQL, projects *project.SQLRegistry, power *hostpower.Controller, states events.SessionStateSource, pending store.PromptPendingSource, listing ProjectSessions, resources ResourceLifetime) (*Runtime, error) {
	b := &Runtime{}

	b.Hub = events.WrapDebugHub(events.NewMemoryHub())
	b.Outbox = eventoutbox.New(database, b.Hub)
	if b.Outbox != nil {
		resources.Track("event-outbox", 120, func(context.Context) error { return b.Outbox.Close() })
	}
	// An absent outbox would silently drop mutation events.
	if b.Outbox == nil {
		return nil, fmt.Errorf("event outbox: nil after construction; every store wired below would drop its events")
	}
	sessions.SetEventOutbox(b.Outbox)
	projects.SetEventOutbox(b.Outbox)
	b.Presence = events.NewPresence(b.Hub, events.DefaultUserActionWindow)
	eventLookup := project.ScopeLookup{Registry: projects}
	b.Publisher = &events.Publisher{
		Hub:               b.Hub,
		Lookup:            eventLookup,
		Untrusted:         sessions,
		UserTurns:         sessions,
		ActivityObserver:  power,
		TurnClockObserver: power,
		SessionProject: func(ctx context.Context, sessionID string) (string, bool) {
			if sessions == nil {
				return "", false
			}
			sess, err := sessions.Get(ctx, sessionID)
			if err != nil || sess == nil || strings.TrimSpace(sess.ProjectID) == "" {
				return "", false
			}
			return sess.ProjectID, true
		},
		SessionLister: events.FuncSessionLister(func(ctx context.Context, projectID string) ([]string, error) {
			if listing == nil {
				return nil, nil
			}
			page, err := listing.ListProjectSessions(ctx, store.SummaryQuery{ProjectID: projectID, Limit: 64})
			if err != nil {
				return nil, err
			}
			ids := make([]string, 0, len(page.Sessions))
			for _, summary := range page.Sessions {
				ids = append(ids, summary.ID)
			}
			return ids, nil
		}),
		SessionRoots: events.FuncSessionRoots(func(ctx context.Context, sessionID string) (string, bool) {
			if sessions == nil {
				return "", false
			}
			sess, err := sessions.Get(ctx, sessionID)
			if err != nil || sess == nil {
				return "", false
			}
			path := strings.TrimSpace(sess.WorkspacePath)
			if path == "" {
				return "", false
			}
			return path, true
		}),
	}
	resources.Track("event-publisher", 125, b.Publisher.Close)
	// Chats follow their turns from session events; documents arrive with the server.
	b.Agents = agentpresence.New(&presenceChats{store: sessions, workerJobs: b.latestWorker}, b.Publisher)
	b.Publisher.SessionObserver = b.Agents
	// One revision counter across the direct and outbox session-event paths.
	sessions.SetSessionRevisions(b.Publisher)
	// Both paths read prompt_pending from the manager.
	sessions.SetPromptPending(pending)
	b.Publisher.SessionState = states
	b.Outbox.OnDelivered = func(ctx context.Context, delivered eventoutbox.DeliveredEvent) {
		if err := power.ObserveDelivered(delivered.Topic, delivered.Data); err != nil {
			slog.WarnContext(ctx, "observe host power activity", "topic", delivered.Topic, "error", err)
		}
		if err := b.Agents.ObserveDelivered(ctx, delivered.Topic, delivered.Data); err != nil {
			slog.WarnContext(ctx, "observe agent presence", "topic", delivered.Topic, "error", err)
		}
		if events.AttentionLifecycleTopic(delivered.Topic) {
			b.Publisher.PublishAttention(ctx)
		}
	}
	b.Outbox.Start(ctx)

	unbind := sourcefeed.Bind(outboxSourceFeed{outbox: b.Outbox, lookup: eventLookup})
	resources.Track("source-feeds", 80, func(context.Context) error { unbind(); return nil })
	return b, nil
}
