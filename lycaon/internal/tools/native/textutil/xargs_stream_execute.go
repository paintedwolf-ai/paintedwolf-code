package textutil

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

type xargsNext struct {
	argv commandprogram.Fields
	err  error
}

// execute overlaps bounded input parsing with active children. Finished
// parallel batches are delivered in order even while the input producer waits.
func (x xargsRun) execute(input *xargsInput, plan xargsPlan) error {
	batcher, err := newXargsBatcher(input, plan)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(x.inv.ctx)
	input.ctx = ctx
	next := make(chan xargsNext)
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		defer close(next)
		for {
			argv, err := batcher.next()
			if errors.Is(err, io.EOF) {
				return
			}
			select {
			case next <- xargsNext{argv: argv, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	var active []*batchRun
	held := map[*batchRun]func(){}
	defer func() {
		cancel()
		_ = x.inv.streams.Input.Close()
		workers.Wait()
		for _, run := range active {
			held[run]()
			if run.out != nil {
				run.out.release()
				run.err.release()
			}
		}
	}()
	var inputErr error
	for next != nil || len(active) > 0 {
		incoming := next
		if len(active) >= plan.parallel {
			incoming = nil
		}
		var done <-chan struct{}
		if len(active) > 0 {
			done = active[0].done
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case item, open := <-incoming:
			if !open {
				next = nil
				continue
			}
			if item.err != nil {
				inputErr, next = item.err, nil
				continue
			}
			if x.inv.ports.Nested == nil {
				return unsupported(x.inv, item.argv[0], "a program runtime that dispatches xargs batches")
			}
			size := int64(128 * len(item.argv))
			for _, word := range item.argv {
				size += int64(len(word.Value))
			}
			release, err := x.inv.ports.Memory.Reserve(ctx, size)
			if err != nil {
				return err
			}
			run := &batchRun{argv: item.argv, done: make(chan struct{})}
			held[run] = release
			if plan.parallel > 1 {
				run.out, run.err = newMeteredBuffer(x.inv), newMeteredBuffer(x.inv)
			}
			active = append(active, run)
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer close(run.done)
				streams := commandprogram.Streams{Input: commandprogram.NullInput(), Output: x.inv.out, Error: x.inv.streams.Error}
				if run.out != nil {
					streams.Output, streams.Error = run.out, run.err
				}
				x.trace(streams.Error, run.argv)
				run.result, run.failure = x.inv.ports.Nested.RunNested(ctx, x.inv.request, run.argv, x.environment(), streams)
			}()
		case <-done:
			run := active[0]
			stop, err := x.settle(run)
			if err == nil && run.out != nil {
				err = x.deliver(run)
			}
			if run.out != nil {
				run.out.release()
				run.err.release()
			}
			held[run]()
			delete(held, run)
			active[0] = nil
			active = active[1:]
			if stop || err != nil {
				return err
			}
		}
	}
	return inputErr
}
