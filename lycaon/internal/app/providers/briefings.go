package providers

import (
	"context"

	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/briefingstore"
	"github.com/lycaon/lycaon/internal/db"
)

// Briefings is the host's one briefing engine: every profile shares its
// scheduler, store and stored results.
type Briefings struct {
	Scheduler *briefing.Scheduler
	Store     *briefingstore.Store
	Engine    *briefing.Engine
	Router    *briefing.Router
}

func (b *Runtime) buildBriefings(ctx context.Context, database *db.Store, dataDir string, resources ResourceLifetime, recovery *bootrecovery.Registry) error {
	scheduler := briefing.NewScheduler(ctx)
	store := briefingstore.NewStore(database, dataDir)
	router := briefing.NewRouter()
	b.Briefings = &Briefings{Scheduler: scheduler, Store: store, Router: router,
		Engine: briefing.NewEngine(scheduler, store, briefing.NewModelGenerator(b.Service, b.Costs), router, nil)}
	resources.Track("briefings", 46, func(ctx context.Context) error {
		scheduler.Stop()
		scheduler.Wait(ctx)
		return nil
	})
	// No attempt survives a restart, so pending ones end before work is admitted.
	return recovery.Register(bootrecovery.Entry{
		Name: "briefing-attempts", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild,
		Run: func(ctx context.Context) error {
			if _, err := store.FailInterrupted(ctx); err != nil {
				return err
			}
			return store.Sweep(ctx)
		},
	})
}
