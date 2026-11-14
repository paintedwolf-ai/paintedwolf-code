package nativegen

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

func TestRequestPriorityFollowsUrgency(t *testing.T) {
	if got := requestPriority(modelcall.CompletionRequest{}); got != inference.Foreground {
		t.Fatalf("interactive request priority=%v", got)
	}
	utility := modelcall.CompletionRequest{Composition: modelcall.CompositionHostUtility}
	if got := requestPriority(utility); got != inference.Foreground {
		t.Fatalf("awaited utility request priority=%v", got)
	}
	utility.Urgency = modelcall.UrgencyBackground
	if got := requestPriority(utility); got != inference.Background {
		t.Fatalf("background request priority=%v", got)
	}
}

func TestForegroundPreemptsPreparationAndRetainsRuntime(t *testing.T) {
	runtime := NewRuntime(RuntimeConfig{})
	defer runtime.Close()
	background, release, ok := runtime.acquirePreparation(t.Context())
	if !ok {
		t.Fatal("preparation admission refused")
	}
	if _, _, duplicate := runtime.acquirePreparation(t.Context()); duplicate {
		t.Fatal("admitted overlapping preparation")
	}
	acquired := make(chan func(), 1)
	go func() {
		_, finish, err := runtime.acquire(t.Context())
		if err != nil {
			acquired <- nil
			return
		}
		acquired <- finish
	}()
	<-background.Done()
	select {
	case <-acquired:
		t.Fatal("foreground entered before preparation settled")
	default:
	}
	release()
	finish := <-acquired
	if finish == nil {
		t.Fatal("foreground admission failed")
	}
	if _, _, accepted := runtime.acquirePreparation(t.Context()); accepted {
		t.Fatal("preparation displaced foreground")
	}
	finish()
	_, release, ok = runtime.acquirePreparation(t.Context())
	if !ok {
		t.Fatal("idle preparation did not resume")
	}
	release()
}

func TestCanceledAdmissionDoesNotRetainTheGate(t *testing.T) {
	runtime := NewRuntime(RuntimeConfig{})
	defer runtime.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := runtime.acquire(ctx); err == nil {
		t.Fatal("accepted canceled foreground")
	}
	if _, _, ok := runtime.acquirePreparation(ctx); ok {
		t.Fatal("accepted canceled preparation")
	}
	_, release, err := runtime.acquire(t.Context())
	if err != nil {
		t.Fatalf("gate not released: %v", err)
	}
	release()
}
