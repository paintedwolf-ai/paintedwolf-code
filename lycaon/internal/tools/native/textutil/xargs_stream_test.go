//go:build darwin || linux

package textutil

import (
	"context"
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
