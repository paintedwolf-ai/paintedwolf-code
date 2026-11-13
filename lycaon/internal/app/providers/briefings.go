package providers

import (
	"context"

	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/briefinghost"
	"github.com/lycaon/lycaon/internal/db"
)

func (b *Runtime) buildBriefings(ctx context.Context, database *db.Store, dataDir string, resources ResourceLifetime, recovery *bootrecovery.Registry) error {
	host := briefinghost.New(ctx, database, dataDir, briefing.NewModelGenerator(b.Service, b.Costs))
	b.Briefings = host
	resources.Track("briefings", 46, host.Close)
	return recovery.Register(bootrecovery.Entry{
		Name: "briefing-attempts", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild, Run: host.Recover,
	})
}
