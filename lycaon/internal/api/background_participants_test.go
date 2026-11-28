package api

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/filebriefing"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/coordinatorcontrol"
	"github.com/lycaon/lycaon/internal/session/curation"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/turnadmission"
	"github.com/lycaon/lycaon/internal/session/turnexecution"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// silentTriggerOutbox invokes onFirstPass during its first Wait without
// advancing its own Epoch or reporting Busy, so WaitForBackground only runs
// another pass if the target participant's own Epoch/Busy signal catches the
// newly admitted work.
type silentTriggerOutbox struct {
	calls       atomic.Int64
	onFirstPass func(context.Context)
}

func (o *silentTriggerOutbox) Wait(ctx context.Context) error {
	if o.calls.Add(1) == 1 && o.onFirstPass != nil {
		o.onFirstPass(ctx)
	}
	return ctx.Err()
}

func (*silentTriggerOutbox) Epoch() uint64 { return 0 }
func (*silentTriggerOutbox) Busy() bool    { return false }

func assertSecondPassAfterHandoff(t *testing.T, s *Server, trigger *silentTriggerOutbox, entered, release chan struct{}) {
	t.Helper()
	drained := make(chan error, 1)
	go func() { drained <- s.WaitForBackground(t.Context()) }()

	select {
	case <-entered:
	case err := <-drained:
		t.Fatalf("WaitForBackground returned (%v) before admitted work started", err)
	case <-t.Context().Done():
		t.Fatal("admitted work never started")
	}

	select {
	case err := <-drained:
		t.Fatalf("WaitForBackground returned (%v) while admitted work was still held", err)
	default:
	}

	close(release)
	select {
	case err := <-drained:
		testutil.FailErr(t, "WaitForBackground after release", err)
	case <-t.Context().Done():
		t.Fatal("WaitForBackground did not finish after release")
	}
	if got := trigger.calls.Load(); got < 2 {
		t.Fatalf("WaitForBackground ran %d passes, want >= 2", got)
	}
}

type watchStubRegistry struct {
	project.Registry
	proj *project.Project
}

func (r watchStubRegistry) Get(context.Context, string) (*project.Project, error) {
	return r.proj, nil
}

func TestWaitForBackgroundRepeatsWhenWatchAdmitsAfterWait(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var hold atomic.Bool
	var once sync.Once
	dir := t.TempDir()
	proj := &project.Project{
		ID:    "proj-watch",
		Roots: []project.Root{{ID: "root-1", Path: dir}},
	}
	watch := &sourceapi.Watch{
		ProjectRegistry: watchStubRegistry{proj: proj},
		WatchNeedsSeed: func(string) bool {
			if hold.Load() {
				once.Do(func() { close(entered) })
				<-release
			}
			return false
		},
	}
	// Pre-bind the watch so the in-pass EnsureSourceWatch call only enters
	// watchWork without scheduling background inventory.
	watch.EnsureSourceWatch(t.Context(), proj.ID)
	hold.Store(true)

	trigger := &silentTriggerOutbox{
		onFirstPass: func(ctx context.Context) {
			go watch.EnsureSourceWatch(ctx, proj.ID)
			<-entered
		},
	}
	s := &Server{eventOutbox: trigger}
	s.Sources.Watch = watch
	t.Cleanup(func() { s.StopBackground(t.Context()) })

	assertSecondPassAfterHandoff(t, s, trigger, entered, release)
}

func TestWaitForBackgroundRepeatsWhenFileBriefingAdmitsAfterWait(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	scheduler := briefing.NewScheduler(t.Context())
	fb := filebriefing.NewService(t.Context(), filebriefing.Dependencies{Scheduler: scheduler})
	trigger := &silentTriggerOutbox{
		onFirstPass: func(ctx context.Context) {
			_, err := scheduler.Submit(ctx, briefing.Job{
				Key:    "file\x00test",
				Policy: briefing.ApprovalPolicy,
				Run: func(context.Context) {
					close(entered)
					<-release
				},
			})
			testutil.FailErr(t, "submit file briefing job", err)
			<-entered
		},
	}
	s := &Server{fileBriefings: fb, eventOutbox: trigger}
	t.Cleanup(func() { s.StopBackground(t.Context()) })

	assertSecondPassAfterHandoff(t, s, trigger, entered, release)
}

type stubAdmissionSessions struct{}

func (stubAdmissionSessions) Get(context.Context, string) (*wire.Session, error) {
	return &wire.Session{ID: "sess-1"}, nil
}

type stubAdmissionSubmissions struct {
	queued []sessionstore.PromptSubmission
}

func (s *stubAdmissionSubmissions) ListQueuedUserPromptSubmissions(context.Context, string) ([]sessionstore.PromptSubmission, error) {
	return s.queued, nil
}

type blockingDrainSubmissions struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingDrainSubmissions) DrainPromptSubmissions(context.Context, string) error {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return nil
}

func TestWaitForBackgroundRepeatsWhenCoordinatorTurnAdmitsAfterWait(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	admission := turnadmission.New(
		stubAdmissionSessions{},
		&stubAdmissionSubmissions{queued: []sessionstore.PromptSubmission{{ID: "sub-1"}}},
		lifecycle.New(nil),
		nil, nil, nil,
	)
	admission.Bind(nil, nil, &blockingDrainSubmissions{entered: entered, release: release})

	trigger := &silentTriggerOutbox{
		onFirstPass: func(ctx context.Context) {
			admission.RoundEnd(ctx, "sess-1")
			<-entered
		},
	}
	s := &Server{
		sessions: &session.Host{
			Coordinator: &coordinatorcontrol.Service{Admission: admission},
		},
		eventOutbox: trigger,
	}
	t.Cleanup(func() { s.StopBackground(t.Context()) })

	assertSecondPassAfterHandoff(t, s, trigger, entered, release)
}

type allowCurationAdmission struct{}

func (allowCurationAdmission) WithSessionTreeAdmission(_ context.Context, _ string, fn func() error) error {
	return fn()
}

type noopCurationNaming struct{}

func (noopCurationNaming) SessionFromPrompt(context.Context, *wire.Session, string) {}
func (noopCurationNaming) ProjectFromPrompt(context.Context, *wire.Session, string) {}

type blockingCurationWarming struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingCurationWarming) DeclaredURLs(context.Context, string, string, string, string) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
}

func TestWaitForBackgroundRepeatsWhenCurationAdmitsAfterWait(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	cur := curation.New(nil, nil, allowCurationAdmission{}, noopCurationNaming{}, &blockingCurationWarming{entered: entered, release: release})
	t.Cleanup(cur.Stop)

	trigger := &silentTriggerOutbox{
		onFirstPass: func(ctx context.Context) {
			cur.Kick(ctx, &wire.Session{ID: "sess-curation"}, "curate this prompt")
			<-entered
		},
	}
	s := &Server{
		sessions: &session.Host{
			Runner: &turnexecution.Service{Curation: cur},
		},
		eventOutbox: trigger,
	}
	t.Cleanup(func() { s.StopBackground(t.Context()) })

	assertSecondPassAfterHandoff(t, s, trigger, entered, release)
}

type blockingRepoCatalog struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingRepoCatalog) OpenIndex(context.Context, string, sourcecatalog.Root, time.Duration) (*sourcecatalog.IndexReader, sourcecatalog.TreeStatus, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return nil, sourcecatalog.TreeStatus{}, context.Canceled
}

func (*blockingRepoCatalog) IndexStatus(context.Context, string, sourcecatalog.Root) (sourcecatalog.TreeStatus, error) {
	return sourcecatalog.TreeStatus{}, nil
}

func (b *blockingRepoCatalog) RootFileCount(context.Context, string, sourcecatalog.Root, sourcecatalog.FileScope, time.Duration) (sourcecatalog.RootFiles, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return sourcecatalog.RootFiles{}, context.Canceled
}

func TestWaitForBackgroundRepeatsWhenRepoProviderAdmitsAfterWait(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	dir := t.TempDir()
	provider := repoinfo.NewProvider(
		&blockingRepoCatalog{entered: entered, release: release},
		func(context.Context, string) (repoinfo.CatalogRoot, bool, error) {
			return repoinfo.CatalogRoot{ProjectID: "p1", RootID: "r1"}, true, nil
		},
		t.TempDir(),
	)
	t.Cleanup(func() { _ = provider.Close() })

	trigger := &silentTriggerOutbox{
		onFirstPass: func(context.Context) {
			provider.Warm(dir)
			<-entered
		},
	}
	s := &Server{repoProvider: provider, eventOutbox: trigger}
	t.Cleanup(func() { s.StopBackground(t.Context()) })

	assertSecondPassAfterHandoff(t, s, trigger, entered, release)
}
