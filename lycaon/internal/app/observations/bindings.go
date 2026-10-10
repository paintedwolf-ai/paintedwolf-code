package observations

import (
	"context"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/preflight"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"strings"
)

type RepositoryChanges interface{ Changed(context.Context, string) }

func Bind(publisher *events.Publisher, sessions *store.SQL, progressStore progress.RunScopedStore, runs runstate.RunsRepository, settled func(context.Context, string), repository RepositoryChanges, invalidateAge func(string), service *llm.Service) func() {
	if service != nil {
		service.Lifecycle = utilityLanePublisher{pub: publisher}
		if service.Utility != nil {
			service.Utility.SetOnChange(func(llm.SlotSnapshot) { publisher.PublishPreflight(context.Background(), preflight.ProbeLiteSlot) })
		}
	}
	releaseFindings := findings.RegisterAppendObserver(func(ctx context.Context, ev findings.AppendEvent) {
		if strings.TrimSpace(ev.SessionID) == "" {
			return
		}
		publisher.PublishFindings(ctx, ev.SessionID, findings.BumpRevision(ev.SessionID))
	})
	releaseRepository := repochange.RegisterObserver(func(ctx context.Context, ev repochange.Event) {
		if repository != nil {
			repository.Changed(ctx, ev.ProjectDir)
		}
		if ev.Kind == repochange.HeadMoved {
			invalidateAge(ev.ProjectDir)
		}
	})
	activeRun := activeRunIDFromWorkflow(runs)
	coalescer := progress.NewCoalescer(progress.DefaultCoalesceWindow, newProgressChangeEmitter(sessions, publisher, activeRun, progressStore))
	releaseProgress := progress.RegisterWriteObserver(func(ctx context.Context, ev progress.WriteEvent) {
		if strings.TrimSpace(ev.SessionID) == "" {
			return
		}
		publisher.PublishProgress(ctx, ev.SessionID, progress.BumpRevision(ev.SessionID))
		coalescer.Record(ev.SessionID, ev.Prev, progressStore.Get(ctx, ev.SessionID))
		emitProgressCompletion(ctx, sessions, publisher, progressStore, activeRun, ev.SessionID)
		settled(ctx, ev.SessionID)
	})
	return func() {
		releaseProgress()
		releaseRepository()
		releaseFindings()
	}
}
