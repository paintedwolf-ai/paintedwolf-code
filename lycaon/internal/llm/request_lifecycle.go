package llm

import (
	"context"
	"log/slog"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

type requestLifecycleKey struct{}

type requestLifecycle struct {
	started    time.Time
	attrs      []any
	dispatch   sync.Once
	dispatched atomic.Bool
	attempt    atomic.Int64
}

func (t *requestLifecycle) record(ctx context.Context, phase string, attrs ...any) {
	if t == nil {
		return
	}
	fields := append([]any(nil), t.attrs...)
	fields = append(fields, "phase", phase, "elapsed_ms", time.Since(t.started).Milliseconds())
	fields = append(fields, attrs...)
	slog.InfoContext(ctx, "model request phase", fields...)
}

func lifecycleFromContext(ctx context.Context) *requestLifecycle {
	trace, _ := ctx.Value(requestLifecycleKey{}).(*requestLifecycle)
	return trace
}

// lifecycleProvider measures preparation as well as transport, without logging payloads.
type lifecycleProvider struct {
	modelcall.Provider
	capacity *modelcall.CapacityGroup
}

func (p *lifecycleProvider) CapacityGroup() *modelcall.CapacityGroup { return p.capacity }

func (p *lifecycleProvider) start(ctx context.Context, req *modelcall.CompletionRequest) (context.Context, *requestLifecycle) {
	callID := firstNonEmpty(req.Debug.CallID, secretmatch.ScreenCallIDFrom(ctx))
	if callID == "" {
		callID = uuid.NewString()
	}
	req.Debug.CallID = callID
	ctx = secretmatch.WithScreenCallID(ctx, callID)
	trace := &requestLifecycle{started: time.Now(), attrs: []any{
		"call_id", callID, "session_id", req.Debug.SessionID,
		"provider_id", p.ID(), "model", req.Model, "purpose", req.Debug.Purpose,
	}}
	ctx = context.WithValue(ctx, requestLifecycleKey{}, trace)
	trace.record(ctx, "preparing")
	return ctx, trace
}

func (p *lifecycleProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	ctx, trace := p.start(ctx, &req)
	out, err := p.Provider.Complete(ctx, req)
	trace.record(ctx, "finished", "dispatched", trace.dispatched.Load(), "failed", err != nil)
	return out, err
}

func (p *lifecycleProvider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ctx, trace := p.start(ctx, &req)
	ch, err := p.Provider.Stream(ctx, req)
	if err != nil {
		trace.record(ctx, "finished", "dispatched", trace.dispatched.Load(), "failed", true)
		return nil, err
	}
	out := make(chan modelcall.StreamChunk, 64)
	go func() {
		defer close(out)
		first, outcome := true, streamOutcomeAbandoned
		for chunk := range ch {
			if chunk.Err != nil {
				outcome = streamOutcomeFailed
			} else if chunk.Done {
				outcome = streamOutcomeCompleted
			}
			if first && streamChunkHasOutput(chunk) {
				trace.record(ctx, "first_output")
				first = false
			}
			if !modelcall.SendChunk(ctx, out, chunk) {
				outcome = streamOutcomeAbandoned
				modelcall.DrainStream(ch)
				break
			}
		}
		trace.record(ctx, "finished", "outcome", outcome, "dispatched", trace.dispatched.Load(), "failed", outcome != streamOutcomeCompleted)
	}()
	return out, nil
}

// streamOutcome is how a stream ended. A consumer that cancels after the
// terminal chunk has still received a completed stream.
type streamOutcome string

const (
	// streamOutcomeCompleted: the terminal chunk arrived without an error.
	streamOutcomeCompleted streamOutcome = "completed"
	// streamOutcomeFailed: the stream carried an error.
	streamOutcomeFailed streamOutcome = "failed"
	// streamOutcomeAbandoned: the stream closed before its terminal chunk, or
	// the consumer stopped reading.
	streamOutcomeAbandoned streamOutcome = "abandoned"
)

// dispatchProvider starts network timing only after local policy and screening.
type dispatchProvider struct{ modelcall.Provider }

func dispatchContext(ctx context.Context) context.Context {
	callAdmissionFrom(ctx).markDispatched()
	notifyDispatch(ctx)
	trace := lifecycleFromContext(ctx)
	if trace == nil {
		secretmatch.EmitPhaseRecord(ctx, secretmatch.ScreenPhaseRecord{
			CallID: secretmatch.ScreenCallIDFrom(ctx),
			Phase:  "provider_dispatch",
		})
		return ctx
	}
	trace.dispatched.Store(true)
	trace.dispatch.Do(func() {
		trace.record(ctx, "dispatch")
		secretmatch.EmitPhaseRecord(ctx, secretmatch.ScreenPhaseRecord{
			CallID: secretmatch.ScreenCallIDFrom(ctx),
			Phase:  "provider_dispatch",
		})
	})
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GetConn:              func(string) { trace.record(ctx, "connection_wait", "http_attempt", trace.attempt.Add(1)) },
		GotConn:              func(info httptrace.GotConnInfo) { trace.record(ctx, "connected", "reused", info.Reused) },
		WroteRequest:         func(info httptrace.WroteRequestInfo) { trace.record(ctx, "request_written", "failed", info.Err != nil) },
		GotFirstResponseByte: func() { trace.record(ctx, "response_started") },
	})
}

func (p *dispatchProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return p.Provider.Complete(dispatchContext(ctx), req)
}

func (p *dispatchProvider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	return p.Provider.Stream(dispatchContext(ctx), req)
}
