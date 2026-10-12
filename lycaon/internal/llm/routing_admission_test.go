package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/testutil"
)

type admissionProvider struct {
	scriptedStreamProvider
	local bool
}

func (p *admissionProvider) Profile() providerprofile.Profile {
	profile := providerprofile.Default()
	profile.UtilitySingleFlight = p.local
	return profile
}

func TestRoutingAdmissionCoversOpeningAndStream(t *testing.T) {
	opening := make(chan struct{})
	opened := make(chan struct{})
	upstream := make(chan modelcall.StreamChunk)
	p := &admissionProvider{local: true, scriptedStreamProvider: scriptedStreamProvider{id: "local", fn: func(int) (<-chan modelcall.StreamChunk, error) {
		close(opening)
		<-opened
		return upstream, nil
	}}}
	client := testRoutingWithProvider(t, p, nil)
	client.plane = NewUtilityPlane()
	client.plane.lanes.overlayWait = time.Millisecond
	result := make(chan (<-chan modelcall.StreamChunk), 1)
	go func() {
		ch, err := client.Stream(t.Context(), modelcall.CompletionRequest{})
		if err != nil {
			t.Error(err)
			return
		}
		result <- ch
	}()
	<-opening
	assertOverlayBusy(t, client.plane, p)
	close(opened)
	ch := <-result
	assertOverlayBusy(t, client.plane, p)
	close(upstream)
	for range ch {
	}
	done, err := client.plane.acquireLane(t.Context(), p, UtilityClassRequested)
	testutil.FailErr(t, "utility after stream", err)
	done()
}

func assertOverlayBusy(t *testing.T, plane *UtilityPlane, p modelcall.Provider) {
	t.Helper()
	summarizer := &RegistrySummarizer{Plane: plane, Class: UtilityClassOverlay, Purpose: "warm_seed"}
	_, err := summarizer.completeUtility(t.Context(), p, "m", modelcall.CompletionRequest{})
	if !errors.Is(err, ErrLiteBusy) {
		t.Fatalf("overlay admission = %v want busy", err)
	}
	if plane.Snapshot().State != SlotReady {
		t.Fatal("admission contention marked lite unavailable")
	}
}

func TestRoutingAdmissionReleasesOpeningFailure(t *testing.T) {
	failed := errors.New("opening failed")
	p := &admissionProvider{local: true, scriptedStreamProvider: scriptedStreamProvider{id: "local", fn: func(int) (<-chan modelcall.StreamChunk, error) { return nil, failed }}}
	client := testRoutingWithProvider(t, p, nil)
	client.plane = NewUtilityPlane()
	_, err := client.Stream(t.Context(), modelcall.CompletionRequest{})
	if !errors.Is(err, failed) {
		t.Fatalf("stream = %v", err)
	}
	done, err := client.plane.acquireLane(t.Context(), p, UtilityClassRequested)
	testutil.FailErr(t, "utility after open failure", err)
	done()
}

func TestRoutingAdmissionPreservesHostedConcurrency(t *testing.T) {
	opening := make(chan struct{})
	opened := make(chan struct{})
	p := &admissionProvider{scriptedStreamProvider: scriptedStreamProvider{id: "hosted", fn: func(int) (<-chan modelcall.StreamChunk, error) {
		close(opening)
		<-opened
		return okStream(), nil
	}}}
	client := testRoutingWithProvider(t, p, nil)
	client.plane = NewUtilityPlane()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, err := client.Complete(t.Context(), modelcall.CompletionRequest{})
		if err != nil {
			t.Error(err)
		}
	}()
	<-opening
	done, err := client.plane.acquireLane(t.Context(), p, UtilityClassOverlay)
	testutil.FailErr(t, "hosted utility during foreground", err)
	done()
	close(opened)
	<-finished
}

func TestStreamAdmissionDrainsBeforeRelease(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	upstream := make(chan modelcall.StreamChunk)
	released := make(chan struct{})
	out := releaseAfterStream(ctx, upstream, func() { close(released) })
	cancel()
	upstream <- modelcall.StreamChunk{Content: "abandoned"}
	select {
	case <-released:
		t.Fatal("admission released before upstream closed")
	default:
	}
	close(upstream)
	for range out {
	}
	<-released
}

func TestStreamAdmissionHandlesAbsentStream(t *testing.T) {
	released := false
	if out := releaseAfterStream(t.Context(), nil, func() { released = true }); out != nil || !released {
		t.Fatal("absent stream retained admission")
	}
	upstream := okStream()
	if out := releaseAfterStream(t.Context(), upstream, nil); out != upstream {
		t.Fatal("unrestricted stream was wrapped")
	}
}
