package filebriefing

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testutil"
)

type generationFunc func(context.Context, GenerationRequest, func(string)) (string, error)

func (f generationFunc) Generate(ctx context.Context, req GenerationRequest, emit func(string)) (string, error) {
	return f(ctx, req, emit)
}

type memoryEnablement struct{ enabled atomic.Bool }

func (s *memoryEnablement) Enabled() bool               { return s.enabled.Load() }
func (s *memoryEnablement) PutEnabled(value bool) error { s.enabled.Store(value); return nil }

func serviceTarget(path string) Target {
	return Target{ProjectID: "project", RootID: "root", Input: Input{Path: path, Presentation: "current", Source: "package main\nfunc Build() {}\n", SourceSHA256: "source"}}
}

func serviceFixture(t *testing.T, generate Generator) (*Service, *Memory, *memoryEnablement) {
	t.Helper()
	cfg, err := LoadConfig()
	testutil.FailErr(t, "load config", err)
	enabled := &memoryEnablement{}
	enabled.enabled.Store(true)
	store := NewMemory()
	service := NewService(t.Context(), Dependencies{Config: cfg, Store: store, Settings: enabled, Generator: generate})
	t.Cleanup(func() {
		service.Stop()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		service.Wait(ctx)
	})
	return service, store, enabled
}

func awaitGeneration(t *testing.T, started <-chan context.Context) context.Context {
	t.Helper()
	select {
	case ctx := <-started:
		return ctx
	case <-time.After(time.Second):
		t.Fatal("generation did not start")
		return nil
	}
}

func TestGetCoalescesPendingAttemptAndRequestCancellationDoesNotStopIt(t *testing.T) {
	t.Parallel()
	started := make(chan context.Context, 2)
	service, _, _ := serviceFixture(t, generationFunc(func(ctx context.Context, _ GenerationRequest, _ func(string)) (string, error) {
		started <- ctx
		<-ctx.Done()
		return "", ctx.Err()
	}))
	requestCtx, cancel := context.WithCancel(t.Context())
	target := serviceTarget("main.go")
	initial, err := service.Request(requestCtx, target, "manual")
	testutil.FailErr(t, "request briefing", err)
	generationCtx := awaitGeneration(t, started)
	cancel()
	for range 8 {
		found, err := service.Get(t.Context(), target)
		testutil.FailErr(t, "resume pending briefing", err)
		if found.AttemptID != initial.AttemptID {
			t.Fatal("pending lookup replaced the attempt")
		}
	}
	if generationCtx.Err() != nil {
		t.Fatal("request cancellation stopped generation")
	}
	select {
	case <-started:
		t.Fatal("pending lookups started another generator")
	default:
	}
	service.Stop()
	select {
	case <-generationCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel generation")
	}
	if _, err := service.Request(t.Context(), target, "manual"); !errors.Is(err, ErrStopped) {
		t.Fatalf("request after shutdown: %v", err)
	}
}

func TestAutomaticSupersessionLeavesManualJobRunning(t *testing.T) {
	t.Parallel()
	started := make(chan context.Context, 3)
	service, _, _ := serviceFixture(t, generationFunc(func(ctx context.Context, _ GenerationRequest, _ func(string)) (string, error) {
		started <- ctx
		<-ctx.Done()
		return "", ctx.Err()
	}))
	_, err := service.Request(t.Context(), serviceTarget("manual.go"), "manual")
	testutil.FailErr(t, "request manual", err)
	manual := awaitGeneration(t, started)
	_, err = service.Request(t.Context(), serviceTarget("first.go"), "automatic")
	testutil.FailErr(t, "request first automatic", err)
	first := awaitGeneration(t, started)
	_, err = service.Request(t.Context(), serviceTarget("second.go"), "automatic")
	testutil.FailErr(t, "request second automatic", err)
	second := awaitGeneration(t, started)
	if first.Err() == nil {
		t.Fatal("first automatic job was not canceled")
	}
	if manual.Err() != nil || second.Err() != nil {
		t.Fatal("supersession canceled an independent job")
	}
}

func TestDisablePurgesAndFencesLateGenerationAcrossReenable(t *testing.T) {
	t.Parallel()
	started := make(chan context.Context, 1)
	release := make(chan struct{})
	service, store, _ := serviceFixture(t, generationFunc(func(ctx context.Context, _ GenerationRequest, emit func(string)) (string, error) {
		started <- ctx
		<-release
		emit("Late model output.")
		return "Late model output.", nil
	}))
	projectID := uuid.NewString()
	hub := events.NewMemoryHub()
	service.events = hub
	stream, unsubscribe, err := hub.Subscribe(t.Context(), events.Subscription{Project: projectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	defer unsubscribe()
	target := serviceTarget("main.go")
	target.ProjectID = projectID
	first, err := service.Request(t.Context(), target, "manual")
	testutil.FailErr(t, "request", err)
	generation := awaitGeneration(t, started)
	testutil.FailErr(t, "disable", service.SetEnabled(t.Context(), false))
	if generation.Err() == nil {
		t.Fatal("disable did not cancel generation")
	}
	testutil.FailErr(t, "reenable", service.SetEnabled(t.Context(), true))
	close(release)
	service.Wait(t.Context())
	if _, err := store.Get(t.Context(), first.ProjectID, first.RootID, first.Path, first.TargetKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("late generation recreated purged record: %v", err)
	}
	// Re-enabling summaries does not admit results from canceled jobs.
	select {
	case <-stream:
	default:
		t.Fatal("missing pending event")
	}
	select {
	case event := <-stream:
		t.Fatalf("late event: %s", event.Data)
	default:
	}
}

func TestStopFencesLateResultAndLeavesAttemptResumable(t *testing.T) {
	t.Parallel()
	started := make(chan context.Context, 1)
	release := make(chan struct{})
	service, store, _ := serviceFixture(t, generationFunc(func(ctx context.Context, _ GenerationRequest, emit func(string)) (string, error) {
		started <- ctx
		<-release
		emit("Purpose: Build the application.")
		return "Purpose: Build the application.", nil
	}))
	target := serviceTarget("main.go")
	first, err := service.Request(t.Context(), target, "manual")
	testutil.FailErr(t, "request", err)
	awaitGeneration(t, started)
	service.Stop()
	close(release)
	service.Wait(t.Context())
	found, err := store.Get(t.Context(), first.ProjectID, first.RootID, first.Path, first.TargetKey)
	testutil.FailErr(t, "read interrupted attempt", err)
	if found.Status != StatusPending || found.AttemptID != first.AttemptID {
		t.Fatalf("shutdown settled an interrupted attempt: %+v", found)
	}
	resumed := make(chan context.Context, 1)
	next := NewService(t.Context(), Dependencies{Store: store, Config: service.config,
		Generator: generationFunc(func(ctx context.Context, _ GenerationRequest, _ func(string)) (string, error) {
			resumed <- ctx
			return "Purpose: Build the application.", nil
		}),
	})
	t.Cleanup(next.Stop)
	_, err = next.Get(t.Context(), target)
	testutil.FailErr(t, "resume interrupted attempt", err)
	awaitGeneration(t, resumed)
	next.Wait(t.Context())
	found, err = store.Get(t.Context(), first.ProjectID, first.RootID, first.Path, first.TargetKey)
	testutil.FailErr(t, "read resumed attempt", err)
	if found.Status == StatusPending {
		t.Fatal("resumed attempt did not settle")
	}
}

func TestReturningToSupersededBriefingDoesNotWaitForRetiredGenerator(t *testing.T) {
	t.Parallel()
	started := make(chan context.Context, 3)
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	service, _, _ := serviceFixture(t, generationFunc(func(ctx context.Context, _ GenerationRequest, _ func(string)) (string, error) {
		index := calls.Add(1)
		started <- ctx
		if index == 1 {
			<-releaseFirst
		} else {
			<-ctx.Done()
		}
		return "", ctx.Err()
	}))
	target := serviceTarget("first.go")
	first, err := service.Request(t.Context(), target, "automatic")
	testutil.FailErr(t, "request first", err)
	retired := awaitGeneration(t, started)
	_, err = service.Request(t.Context(), serviceTarget("second.go"), "automatic")
	testutil.FailErr(t, "request second", err)
	second := awaitGeneration(t, started)
	if retired.Err() == nil {
		t.Fatal("first job was not superseded")
	}
	resumed, err := service.Get(t.Context(), target)
	testutil.FailErr(t, "return to first", err)
	current := awaitGeneration(t, started)
	close(releaseFirst)
	if resumed.AttemptID != first.AttemptID {
		t.Fatal("resume replaced the durable attempt")
	}
	if current.Err() != nil || second.Err() == nil {
		t.Fatal("resume did not replace the canceled automatic generation")
	}
}
