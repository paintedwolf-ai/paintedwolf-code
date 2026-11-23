package observations

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/preflight"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/internal/workscope"
)

type RepositoryChanges interface{ Changed(context.Context, string) }

func Bind(publisher *events.Publisher, sessions *store.Repositories, progressStore progress.RunScopedStore, runs runstate.RunsRepository, repository RepositoryChanges, service *llm.Service) func(context.Context) error {
	var work workscope.Group
	var queries SessionReader
	var transcript TranscriptWriter
	if sessions != nil {
		queries, transcript = sessions.Sessions, sessions.Transcript
	}
	if service != nil {
		service.Lifecycle = utilityLanePublisher{pub: publisher}
		if service.Utility != nil {
			service.Utility.SetOnChange(func(llm.SlotSnapshot) { publisher.PublishPreflight(context.Background(), preflight.ProbeLiteSlot) })
		}
	}
	releaseFindings := findings.RegisterAppendObserver(scopedObserver(&work, func(ctx context.Context, ev findings.AppendEvent) {
		if strings.TrimSpace(ev.SessionID) == "" {
			return
		}
		publisher.PublishFindings(ctx, ev.SessionID, findings.BumpRevision(ev.SessionID))
	}))
	releaseRepository := repochange.RegisterObserver(scopedObserver(&work, func(ctx context.Context, ev repochange.Event) {
		if repository != nil {
			repository.Changed(ctx, ev.ProjectDir)
		}
	}))
	activeRun := activeRunIDFromWorkflow(runs)
	coalescer := progress.NewCoalescer(progress.DefaultCoalesceWindow, newProgressChangeEmitter(queries, transcript, publisher, activeRun, progressStore))
	releaseProgress := progress.RegisterWriteObserver(scopedObserver(&work, func(ctx context.Context, ev progress.WriteEvent) {
		if strings.TrimSpace(ev.SessionID) == "" {
			return
		}
		publisher.PublishProgress(ctx, ev.SessionID, progress.BumpRevision(ev.SessionID))
		coalescer.Record(ev.SessionID, ev.Before, ev.After)
		emitProgressCompletionFromContent(ctx, queries, transcript, publisher, progressStore, activeRun, ev.SessionID, ev.After.Content)
	}))
	return func(ctx context.Context) error {
		work.Stop()
		releaseProgress()
		releaseRepository()
		releaseFindings()
		if err := work.Wait(ctx); err != nil {
			return err
		}
		coalescer.Close()
		return nil
	}
}

func scopedObserver[T any](work *workscope.Group, observe func(context.Context, T)) func(context.Context, T) {
	return func(ctx context.Context, event T) {
		ctx, finish, err := work.Begin(ctx)
		if err != nil {
			return
		}
		defer finish()
		observe(ctx, event)
	}
}
