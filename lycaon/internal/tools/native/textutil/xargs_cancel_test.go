//go:build darwin || linux

package textutil

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
)

type heldXargsInput struct {
	first                *strings.Reader
	blocked, closed      chan struct{}
	blockOnce, closeOnce sync.Once
}

func (r *heldXargsInput) Read(p []byte) (int, error) {
	if r.first.Len() > 0 {
		return r.first.Read(p)
	}
	r.blockOnce.Do(func() { close(r.blocked) })
	<-r.closed
	return 0, io.ErrClosedPipe
}
func (r *heldXargsInput) Close() error { r.closeOnce.Do(func() { close(r.closed) }); return nil }

type heldXargsChildren struct {
	input                        *heldXargsInput
	parallel                     int
	secondStarted, secondStopped chan struct{}
}

func (r *heldXargsChildren) RunNested(ctx context.Context, _ commandprogram.OperationRequest, argv commandprogram.Fields, _ map[string]string, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	if argv[len(argv)-1].Value == "two" {
		close(r.secondStarted)
		<-ctx.Done()
		close(r.secondStopped)
		return toolcontract.ProducerResult{Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, ctx.Err()
	}
	<-r.input.blocked
	if r.parallel > 1 {
		<-r.secondStarted
	}
	_, err := io.WriteString(streams.Output, "first\n")
	if err != nil {
		return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
	}
	return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
}

func TestXargsEarlyConsumerClosesBlockedInputAndActiveChildren(t *testing.T) {
	for _, parallel := range []int{1, 2} {
		t.Run(map[int]string{1: "serial", 2: "parallel"}[parallel], func(t *testing.T) {
			h := newHarness(t)
			text := "one\n"
			if parallel == 2 {
				text += "two\n"
			}
			input := &heldXargsInput{first: strings.NewReader(text), blocked: make(chan struct{}), closed: make(chan struct{})}
			children := &heldXargsChildren{input: input, parallel: parallel, secondStarted: make(chan struct{}), secondStopped: make(chan struct{})}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			inv := &invocation{ctx: ctx, owners: h.owners, ports: h.ports(), request: commandprogram.OperationRequest{Argv: commandprogram.Fields{{Value: "xargs"}}}, streams: commandprogram.Streams{Input: input, Error: io.Discard}}
			inv.ports.Nested = children
			budget, err := commandprogram.NewByteBudget("fixture", 1024)
			testutil.FailErr(t, "open actual output endpoint", err)
			output, err := commandprogram.NewBytePipe(ctx, budget)
			testutil.FailErr(t, "open actual output endpoint", err)
			testutil.FailErr(t, "close satisfied output consumer", output.CloseConsumer())
			inv.out = nativeutility.NewOutput(ctx, output, "stream_throughput_bytes", inv.limit("stream_throughput_bytes"))
			plan := xargsPlan{count: 1, parallel: parallel, maxChars: inv.limit("argv_bytes")}
			parser := &xargsInput{inv: inv, ctx: ctx, reader: bufio.NewReaderSize(input, 64<<10)}
			done := make(chan error, 1)
			go func() { done <- (xargsRun{inv: inv}).execute(parser, plan) }()
			select {
			case err := <-done:
				if !errors.Is(err, commandprogram.ErrConsumerClosed) {
					t.Fatalf("early close: %v", err)
				}
			case <-time.After(5 * time.Second):
				cancel()
				_ = input.Close()
				<-done
				t.Fatal("early consumer left parser or child blocked")
			}
			select {
			case <-input.closed:
			default:
				t.Fatal("input was not closed")
			}
			if parallel == 2 {
				select {
				case <-children.secondStopped:
				default:
					t.Fatal("active child was not canceled and joined")
				}
			}
			inv.close()
			if h.memory.InUse().Working != 0 {
				t.Fatal("xargs retained working admission")
			}
		})
	}
}
