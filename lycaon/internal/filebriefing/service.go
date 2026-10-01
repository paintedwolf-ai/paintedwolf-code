package filebriefing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/sourceblob"
	wire "github.com/lycaon/lycaon/pkg/api"
)

var (
	ErrDisabled = errors.New("file summaries are off")
	ErrStopped  = errors.New("file briefing service stopped")
)

type EventPublisher interface {
	Publish(context.Context, wire.EventTopic, events.PublishKey, any) error
}

type Enablement interface {
	Enabled() bool
	PutEnabled(bool) error
}

// Target contains one already-authorized, immutable source presentation.
type Target struct {
	ProjectID  string
	RootID     string
	ProjectDir string
	Input      Input
}

type Dependencies struct {
	Store     Store
	Config    Config
	Generator Generator
	Events    EventPublisher
	Settings  Enablement
	Logger    *slog.Logger
}

// Service coordinates briefing admission, generation, retention, and shutdown.
type Service struct {
	store     Store
	config    Config
	generator Generator
	events    EventPublisher
	settings  Enablement
	logger    *slog.Logger
	gate      sync.RWMutex
	stopped   bool
	jobs      sync.Map
	automatic sync.Map
	ctx       context.Context
	cancel    context.CancelFunc
	workers   sync.WaitGroup
}

type generationJob struct {
	context context.Context
	cancel  context.CancelFunc
}

func NewService(ctx context.Context, deps Dependencies) *Service {
	ctx, cancel := context.WithCancel(ctx)
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: deps.Store, config: deps.Config, generator: deps.Generator,
		events: deps.Events, settings: deps.Settings, logger: logger, ctx: ctx, cancel: cancel}
}

func (s *Service) Enabled() bool { return s.settings == nil || s.settings.Enabled() }

func (s *Service) TargetKey(target Target) string {
	descriptor, err := json.Marshal(struct {
		Version                                  int
		Presentation, RootID, Path, SourceSHA256 string
	}{s.config.Version, target.Input.Presentation, target.RootID, target.Input.Path, target.Input.SourceSHA256})
	if err != nil {
		panic(fmt.Errorf("encode file briefing identity: %w", err))
	}
	return sourceblob.ContentSHA(descriptor)
}

func (s *Service) admissionError() error {
	if s.stopped || s.ctx.Err() != nil {
		return ErrStopped
	}
	if !s.Enabled() {
		return ErrDisabled
	}
	return nil
}

func (s *Service) Request(ctx context.Context, target Target, trigger string) (Briefing, error) {
	if trigger != "automatic" && trigger != "manual" {
		return Briefing{}, fmt.Errorf("invalid file briefing trigger %q", trigger)
	}
	material, err := BuildMaterial(ctx, target.Input, s.config)
	if err != nil {
		return Briefing{}, fmt.Errorf("assemble file briefing: %w", err)
	}
	s.gate.Lock()
	defer s.gate.Unlock()
	if err := s.admissionError(); err != nil {
		return Briefing{}, err
	}
	briefing, err := s.store.Start(ctx, Briefing{
		ProjectID: target.ProjectID, RootID: target.RootID, Path: target.Input.Path, TargetKey: s.TargetKey(target),
		Presentation: target.Input.Presentation, SourceSHA256: target.Input.SourceSHA256,
		Trigger: trigger, Status: StatusPending, Preview: material.Preview, Locations: material.Locations, UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		return Briefing{}, err
	}
	s.maintainFileBriefings(ctx, briefing)
	s.ensureJob(ctx, target, briefing, material.Prompt)
	return briefing, nil
}

// Get resumes pending work after interruption using the exact requested source.
func (s *Service) Get(ctx context.Context, target Target) (Briefing, error) {
	s.gate.Lock()
	defer s.gate.Unlock()
	if err := s.admissionError(); err != nil {
		return Briefing{}, err
	}
	briefing, err := s.store.Get(ctx, target.ProjectID, target.RootID, target.Input.Path, s.TargetKey(target))
	if err != nil {
		return Briefing{}, err
	}
	s.maintainFileBriefings(ctx, briefing)
	if briefing.Status == StatusPending {
		material, err := BuildMaterial(ctx, target.Input, s.config)
		if err != nil {
			return Briefing{}, fmt.Errorf("assemble file briefing: %w", err)
		}
		s.ensureJob(ctx, target, briefing, material.Prompt)
	}
	return briefing, nil
}

// SetEnabled serializes purge with every admission and publication.
func (s *Service) SetEnabled(ctx context.Context, enabled bool) error {
	s.gate.Lock()
	defer s.gate.Unlock()
	if s.stopped {
		return ErrStopped
	}
	if s.settings == nil {
		return errors.New("file summaries settings are unavailable")
	}
	if !enabled {
		s.jobs.Range(func(_, value any) bool { value.(*generationJob).cancel(); return true })
		if err := s.store.Clear(ctx); err != nil {
			return err
		}
	}
	return s.settings.PutEnabled(enabled)
}

func (s *Service) ensureJob(ctx context.Context, target Target, briefing Briefing, prompt string) {
	if briefing.Status != StatusPending {
		return
	}
	key := fileBriefingJobKey(briefing)
	jobCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	job := &generationJob{context: jobCtx, cancel: cancel}
	for {
		prior, running := s.jobs.LoadOrStore(key, job)
		if !running {
			break
		}
		previous := prior.(*generationJob)
		if previous.context.Err() == nil {
			cancel()
			return
		}
		// A returning view may resume before its canceled generator has unwound.
		if s.jobs.CompareAndSwap(key, previous, job) {
			break
		}
	}
	s.publishFileBriefingEvent(ctx, fileBriefingEvent(briefing, "pending"))
	if briefing.Trigger == "automatic" {
		if prior, loaded := s.automatic.Swap(briefing.ProjectID, job); loaded {
			prior.(*generationJob).cancel()
		}
	}
	stop := context.AfterFunc(s.ctx, cancel) //nolint:contextcheck // Service shutdown also cancels detached request work.
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		defer observability.GuardPanic("filebriefing.generation")
		defer stop()
		defer cancel()
		defer s.jobs.CompareAndDelete(key, job)
		if briefing.Trigger == "automatic" {
			defer s.automatic.CompareAndDelete(briefing.ProjectID, job)
		}
		s.completeFileBriefing(jobCtx, briefing, prompt, target.ProjectDir)
	}()
}

// Stop closes admission before cancellation so Wait cannot race a new worker.
func (s *Service) Stop() {
	s.gate.Lock()
	s.stopped = true
	s.cancel()
	s.gate.Unlock()
}

func (s *Service) Wait(ctx context.Context) {
	done := make(chan struct{})
	go func() { s.workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
