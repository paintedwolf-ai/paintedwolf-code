package llm

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providers/nativegen"
	"github.com/lycaon/lycaon/internal/testutil"
	"testing"
	"testing/synctest"
)

func TestRegistryAliasesPreemptSharedBackgroundCapacity(t *testing.T) {
	for _, debug := range []string{"", "1"} {
		t.Run("debug="+debug, func(t *testing.T) {
			t.Setenv("LYCAON_LLM_DEBUG", debug)
			checkRegistryAliasCapacity(t)
		})
	}
}

func checkRegistryAliasCapacity(t *testing.T) {
	t.Helper()
	runtime := nativegen.NewRuntime(nativegen.RuntimeConfig{})
	defer runtime.Close()
	registry := &Registry{}
	for _, id := range []string{"coordinator", "utility"} {
		testutil.FailErr(t, "register alias", registry.Register(nativegen.New(id, nativegen.Options{Runtime: runtime})))
	}
	foreground, err := registry.Get("coordinator")
	testutil.FailErr(t, "resolve coordinator", err)
	background, err := registry.Get("utility")
	testutil.FailErr(t, "resolve utility", err)
	if modelcall.CapacityKey(foreground) != modelcall.CapacityKey(background) {
		t.Fatal("registry decorators lost shared runtime identity")
	}
	plane := NewUtilityPlane()
	ctx, release, err := plane.lanes.acquireBackground(t.Context(), modelcall.CapacityKey(background), true)
	testutil.FailErr(t, "start background work", err)
	defer release()
	foregroundRelease, err := plane.acquireForeground(t.Context(), foreground)
	testutil.FailErr(t, "admit foreground", err)
	defer foregroundRelease()
	if ctx.Err() == nil {
		t.Fatal("foreground alias did not cancel background work")
	}
	release()
	plane.lanes.mu.Lock()
	defer plane.lanes.mu.Unlock()
	if len(plane.lanes.lanes) != 1 {
		t.Fatal("aliases created separate lanes")
	}
}

func TestForegroundCancelsTitleAcrossAliases(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				group := modelcall.NewCapacityGroup()
				provider := &cancelableCapacityProvider{group: group, started: make(chan struct{}), id: "lite"}
				foreground := &cancelableCapacityProvider{group: group, id: "coordinator"}
				sum := newTestRegistrySummarizer(t, provider)
				sum.Plane = NewUtilityPlane()
				sum.Class = UtilityClassGift
				done := make(chan error, 1)
				go func() {
					var err error
					if stream {
						_, err = sum.SummarizeStreamOnce(t.Context(), "system", "user", 32, nil)
					} else {
						_, err = sum.completeUtility(t.Context(), provider, "model", modelcall.CompletionRequest{})
					}
					done <- err
				}()
				<-provider.started
				release, err := sum.Plane.acquireForeground(t.Context(), foreground)
				testutil.FailErr(t, "admit foreground during title", err)
				defer release()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatalf("title cancellation = %v", err)
				}
				if sum.Plane.Snapshot().State != SlotReady {
					t.Fatal("preempted title marked the local runtime unavailable")
				}
			})
		})
	}
}

type cancelableCapacityProvider struct {
	modelcall.Provider
	group   *modelcall.CapacityGroup
	id      string
	started chan struct{}
}

func (p *cancelableCapacityProvider) ID() string                              { return p.id }
func (p *cancelableCapacityProvider) CapacityGroup() *modelcall.CapacityGroup { return p.group }
func (p *cancelableCapacityProvider) Profile() providerprofile.Profile {
	return providerprofile.Profile{UtilitySingleFlight: true}
}
func (p *cancelableCapacityProvider) Models() []modelcall.ModelInfo { return nil }
func (p *cancelableCapacityProvider) Complete(ctx context.Context, _ modelcall.CompletionRequest) (*modelcall.Completion, error) {
	close(p.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (p *cancelableCapacityProvider) Stream(ctx context.Context, _ modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	close(p.started)
	chunks := make(chan modelcall.StreamChunk, 1)
	go func() {
		defer close(chunks)
		<-ctx.Done()
		chunks <- modelcall.StreamChunk{Err: ctx.Err()}
	}()
	return chunks, nil
}
