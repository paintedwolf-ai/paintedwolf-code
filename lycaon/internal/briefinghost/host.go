// Package briefinghost runs the host's one briefing engine: every profile
// shares its scheduler, store and stored results.
package briefinghost

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/briefingstore"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Publisher delivers events to subscribers.
type Publisher interface {
	Publish(context.Context, wire.EventTopic, events.PublishKey, any) error
}

type Host struct {
	Scheduler *briefing.Scheduler
	Store     *briefingstore.Store
	Engine    *briefing.Engine
	Router    *briefing.Router
	Registry  *briefing.Registry
}

func New(ctx context.Context, database db.Handle, dataDir string, generator briefing.Generator) *Host {
	scheduler := briefing.NewScheduler(ctx)
	store := briefingstore.NewStore(database, dataDir)
	router := briefing.NewRouter()
	return &Host{Scheduler: scheduler, Store: store, Router: router, Registry: briefing.NewRegistry(),
		Engine: briefing.NewEngine(scheduler, store, generator, router, nil)}
}

// Recover ends attempts no run survived and sweeps what nothing holds. It
// runs before work is admitted.
func (h *Host) Recover(ctx context.Context) error {
	if _, err := h.Store.FailInterrupted(ctx); err != nil {
		return err
	}
	return h.Store.Sweep(ctx)
}

func (h *Host) Close(ctx context.Context) error {
	h.Scheduler.Stop()
	h.Scheduler.Wait(ctx)
	return nil
}

// Publish sends each registered profile's committed snapshots on the
// briefing topic. Profiles the endpoint does not serve publish elsewhere.
func (h *Host) Publish(hub Publisher, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	h.Router.HandleAll(func(ctx context.Context, projectID string, snap briefing.Snapshot) {
		profile, err := h.Registry.Profile(snap.ProfileID)
		if err != nil {
			return
		}
		snapshot, err := h.Store.Snapshot(ctx, briefingstore.SnapshotQuery{ProjectID: projectID, ProfileID: profile.ID(),
			Version: profile.Version(), Policy: profile.Policy(), TargetKey: snap.TargetKey})
		if err == nil {
			err = hub.Publish(ctx, wire.EventTopicBriefing, events.PublishKey{Project: projectID, Facet: snap.ProfileID + "\x00" + snap.TargetKey},
				wire.BriefingEvent{Kind: "snapshot", Snapshot: &snapshot})
		}
		if err != nil {
			logger.WarnContext(ctx, "publish briefing", "profile", snap.ProfileID, "error", err)
		}
	})
}

// Purge removes every registered profile's targets and what only they held,
// in every project, and tells clients to drop earlier epochs.
func (h *Host) Purge(ctx context.Context, hub Publisher) error {
	profiles := h.Registry.IDs()
	h.Scheduler.Cancel(func(job briefing.Job) bool { return h.servesJob(profiles, job) })
	projects, err := h.Store.Projects(ctx)
	if err != nil {
		return err
	}
	for _, projectID := range projects {
		epoch, err := h.Store.Purge(ctx, projectID, profiles)
		if err != nil {
			return err
		}
		if hub == nil {
			continue
		}
		event := wire.BriefingEvent{Kind: "reset", ProfileIds: profiles, CacheEpoch: strconv.FormatInt(epoch, 10)}
		if err := hub.Publish(ctx, wire.EventTopicBriefing, events.PublishKey{Project: projectID, Facet: "reset"}, event); err != nil {
			return err
		}
	}
	return nil
}

func (h *Host) servesJob(profiles []string, job briefing.Job) bool {
	for _, profile := range profiles {
		if briefing.JobProfile(job) == profile {
			return true
		}
	}
	return false
}
