//go:build darwin || linux

package textutil

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/testutil"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type countingBatches struct{ items atomic.Int64 }

func (c *countingBatches) RunNested(_ context.Context, _ commandprogram.OperationRequest, argv commandprogram.Fields, _ map[string]string, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
	c.items.Add(int64(len(argv) - 1))
	return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
}

func TestXargsRetainsOnlyBoundedBatches(t *testing.T) {
	for _, parallel := range []string{"1", "4"} {
		t.Run(parallel, func(t *testing.T) {
			h := newHarness(t)
			counter := &countingBatches{}
			h.nested = counter
			h.run("xargs -n1024 -P"+parallel+" echo", strings.Repeat("item-"+strings.Repeat("x", 95)+"\n", 200000)).expectOutput(t, "streamed batches", "", 0)
			if counter.items.Load() != 200000 {
				t.Fatalf("delivered %d items", counter.items.Load())
			}
			if peak := h.memory.Peak().Working; peak > 8<<20 {
				t.Fatalf("streamed batches retained %d working bytes", peak)
			}
		})
	}
}

func TestXargsLateInvalidItemKeepsCompletedBatches(t *testing.T) {
	h := newHarness(t)
	log := &batchLog{}
	h.nested = log
	r := h.run("xargs -n1 echo", "one two 'unfinished")
	r.expectRefusal(t, "invalid later input", "COMMAND_OPERAND_INVALID")
	if r.stdout != "echo|one\necho|two\n" || len(log.batches) != 2 {
		t.Fatalf("earlier batches lost or repeated: output=%q batches=%v", r.stdout, log.batches)
	}
}

type stoppedParallelBatches struct {
	secondStarted, secondStopped chan struct{}
	result                       toolcontract.ProducerResult
	cause                        error
}

func (b *stoppedParallelBatches) RunNested(ctx context.Context, _ commandprogram.OperationRequest, argv commandprogram.Fields, _ map[string]string, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
	if argv[len(argv)-1].Value == "one" {
		<-b.secondStarted
		return b.result, b.cause
	}
	close(b.secondStarted)
	<-ctx.Done()
	close(b.secondStopped)
	return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.EffectUnknown}, ctx.Err()
}
func TestXargsParallelPreservesStoppedChildAndJoinedFailure(t *testing.T) {
	h := newHarness(t)
	budget, err := commandprogram.NewByteBudget("fixture", 1024)
	testutil.FailErr(t, "admit child byte stream", err)
	pipe, err := commandprogram.NewBytePipe(t.Context(), budget)
	testutil.FailErr(t, "open child byte stream", err)
	testutil.FailErr(t, "close satisfied child consumer", pipe.CloseConsumer())
	_, closed := pipe.Write([]byte("unconsumed"))
	independent := errors.New("independent child failure")
	first := toolcontract.ProducerResult{Status: 7, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.EffectCompleted}
	children := &stoppedParallelBatches{secondStarted: make(chan struct{}), secondStopped: make(chan struct{}), result: first, cause: errors.Join(closed, independent)}
	h.nested = children
	r := h.run("xargs -n1 -P2 echo", "one\ntwo\n")
	if r.result != first || !errors.Is(r.err, closed) || !errors.Is(r.err, independent) {
		t.Fatalf("lost stopped child result=%+v cause=%v", r.result, r.err)
	}
	select {
	case <-children.secondStopped:
	default:
		t.Fatal("later child was not canceled and joined")
	}
	stages := []commandprogram.PipelineStageOutcome{
		{ExecutionID: "xargs", Result: r.result, RequiredInputComplete: true, Closure: commandprogram.NativeConsumerClosure(pipe, r.err, "xargs", "head")},
		{ExecutionID: "head", Result: toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, RequiredInputComplete: true},
	}
	result, err := commandprogram.SettlePipeline("parallel", stages, &commandprogram.OutcomeLatch{})
	testutil.FailErr(t, "settle preserved child failure", err)
	if result.Status != 7 || result.Complete || result.PublicationEligible {
		t.Fatalf("joined failure gained closure authority: %+v", result)
	}
}
