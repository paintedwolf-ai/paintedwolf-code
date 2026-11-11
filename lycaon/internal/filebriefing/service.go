package filebriefing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/events"
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
	// Scheduler is shared with other briefing kinds; nil gives the service
	// its own.
	Scheduler *briefing.Scheduler
	// Records is optional; without it summaries carry no record.
	Records Records
}

// Service coordinates briefing admission, generation, retention, and shutdown.
type Service struct {
	store     Store
	config    Config
	generator Generator
	recorder  summaryRecorder
	events    EventPublisher
	settings  Enablement
	logger    *slog.Logger
	gate      sync.RWMutex
	stopped   bool
	jobs      fileJobs
	ctx       context.Context
	cancel    context.CancelFunc
}

// fileJobPrefix marks this kind's jobs on a shared scheduler.
const fileJobPrefix = "file\x00"

func NewService(ctx context.Context, deps Dependencies) *Service {
	ctx, cancel := context.WithCancel(ctx)
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	scheduler, dedicated := deps.Scheduler, false
	if scheduler == nil {
		scheduler, dedicated = briefing.NewScheduler(ctx), true
	}
	return &Service{store: deps.Store, config: deps.Config, generator: deps.Generator, recorder: summaryRecorder{records: deps.Records, logger: logger},
		events: deps.Events, settings: deps.Settings, logger: logger, jobs: fileJobs{scheduler: scheduler, dedicated: dedicated}, ctx: ctx, cancel: cancel}
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
	s.ensureJob(ctx, target, briefing, material)
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
		s.ensureJob(ctx, target, briefing, material)
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
		s.jobs.cancel()
		if err := s.store.Clear(ctx); err != nil {
			return err
		}
		if err := s.recorder.sweep(ctx); err != nil {
			return err
		}
	}
	return s.settings.PutEnabled(enabled)
}

// ensureJob runs pending work once per attempt. A newer automatic request
// supersedes the project's earlier automatic work; manual work is never
// superseded.
func (s *Service) ensureJob(ctx context.Context, target Target, b Briefing, material Material) {
	if b.Status != StatusPending {
		return
	}
	group := ""
	if b.Trigger == "automatic" {
		group = fileJobPrefix + b.ProjectID
	}
	joined, err := s.jobs.scheduler.Submit(briefing.Job{
		Key: fileJobPrefix + fileBriefingJobKey(b), Project: b.ProjectID, Group: group, Manual: b.Trigger == "manual",
		Policy: briefing.Policy{Admission: briefing.Superseding}, Values: ctx,
		Run: func(jobCtx context.Context) { s.completeFileBriefing(jobCtx, b, material, target.ProjectDir) },
	})
	if err != nil || joined {
		return
	}
	s.publishFileBriefingEvent(ctx, fileBriefingEvent(b, "pending"))
}

// Stop closes admission before cancelling, so Wait cannot race new work.
func (s *Service) Stop() {
	s.gate.Lock()
	s.stopped = true
	s.cancel()
	s.jobs.stop()
	s.gate.Unlock()
}

func (s *Service) Wait(ctx context.Context) { s.jobs.wait(ctx) }

// fileJobs is this kind's use of a scheduler it may share with others.
type fileJobs struct {
	scheduler *briefing.Scheduler
	dedicated bool
}

func (j fileJobs) cancel() {
	j.scheduler.Cancel(func(job briefing.Job) bool { return strings.HasPrefix(job.Key, fileJobPrefix) })
}

// stop ends a dedicated scheduler, or only this kind's jobs on a shared one.
func (j fileJobs) stop() {
	if j.dedicated {
		j.scheduler.Stop()
		return
	}
	j.cancel()
}

func (j fileJobs) wait(ctx context.Context) {
	if j.dedicated {
		j.scheduler.Wait(ctx)
	}
}
