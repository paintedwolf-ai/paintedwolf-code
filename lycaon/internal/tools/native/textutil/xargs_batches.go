package textutil

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
)

// batchStop ends xargs with the outcome of a batch that hit a host stop.
type batchStop struct {
	result toolcontract.ProducerResult
	cause  error
}

func (s *batchStop) Error() string {
	if s.cause != nil {
		return "xargs batch stopped: " + s.cause.Error()
	}
	return "xargs batch stopped: " + string(s.result.Terminal) + " " + s.result.StopCode
}

func (s *batchStop) Unwrap() error { return s.cause }

// batches builds every argv before any runs, so a refused item or batch
// launches nothing. -I replaces the placeholder per line, -L takes that many
// lines, -n that many items; otherwise a batch takes as many items as
// argv_bytes and expanded_fields admit.
func (plan xargsPlan) batches(inv *invocation, command commandprogram.Fields, lines [][]string) ([]commandprogram.Fields, error) {
	argumentLimit := inv.limit("argument_bytes")
	for _, line := range lines {
		for _, item := range line {
			if int64(len(item)) > argumentLimit {
				return nil, nativeutility.LimitExceeded("argument_bytes", argumentLimit, int64(len(item)))
			}
			if strings.IndexByte(item, 0) >= 0 {
				return nil, inv.usage(inv.commandWord(), "items without NUL bytes")
			}
		}
	}
	var out []commandprogram.Fields
	var err error
	switch {
	case plan.replacing:
		out, err = plan.replaced(inv, command, lines)
	case plan.lines > 0:
		for start := 0; start < len(lines) && err == nil; start += plan.lines {
			var items []string
			for _, line := range lines[start:min(start+plan.lines, len(lines))] {
				items = append(items, line...)
			}
			var group []commandprogram.Fields
			group, err = plan.filled(inv, command, items, 0)
			out = append(out, group...)
		}
	default:
		var items []string
		for _, line := range lines {
			items = append(items, line...)
		}
		out, err = plan.filled(inv, command, items, plan.count)
	}
	if err != nil {
		return nil, err
	}
	if limit := inv.limit("child_executions"); int64(len(out)) > limit {
		return nil, nativeutility.LimitExceeded("child_executions", limit, int64(len(out)))
	}
	return out, nil
}

func (plan xargsPlan) replaced(inv *invocation, command commandprogram.Fields, lines [][]string) ([]commandprogram.Fields, error) {
	placeholder := plan.placeholder.Value
	argumentLimit, argvLimit := inv.limit("argument_bytes"), inv.limit("argv_bytes")
	var out []commandprogram.Fields
	for _, line := range lines {
		item := line[0]
		argv := commandprogram.Fields{command[0]}
		size := int64(len(command[0].Value))
		for _, word := range command[1:] {
			value := strings.ReplaceAll(word.Value, placeholder, item)
			if int64(len(value)) > argumentLimit {
				return nil, nativeutility.LimitExceeded("argument_bytes", argumentLimit, int64(len(value)))
			}
			size += int64(len(value))
			argv = append(argv, commandprogram.Field{Value: value, Span: word.Span})
		}
		if size > argvLimit {
			return nil, nativeutility.LimitExceeded("argv_bytes", argvLimit, size)
		}
		out = append(out, argv)
	}
	return out, nil
}

// filled packs items into batches of at most count items (0: unbounded)
// under argv_bytes, -s and expanded_fields. Each batch's items follow the
// command, or replace BSD -J's placeholder argument. With -x a batch cut short
// of its -n count by the size cap refuses.
func (plan xargsPlan) filled(inv *invocation, command commandprogram.Fields, items []string, count int) ([]commandprogram.Fields, error) {
	span := inv.request.Invocation.Span
	fieldLimit := inv.limit("expanded_fields")
	base := int64(0)
	for _, word := range command {
		base += int64(len(word.Value))
	}
	at := len(command)
	if plan.join != "" {
		for i, word := range command[1:] {
			if word.Value == plan.join {
				at, base = i+1, base-int64(len(word.Value))
				break
			}
		}
	}
	var out []commandprogram.Fields
	var current commandprogram.Fields
	size := base
	flush := func() {
		if len(current) > 0 {
			argv := append(append(commandprogram.Fields{}, command[:at]...), current...)
			if at < len(command) {
				argv = append(argv, command[at+1:]...)
			}
			out = append(out, argv)
			current, size = nil, base
		}
	}
	for _, item := range items {
		next := int64(len(item))
		full := count > 0 && len(current) == count
		tooLarge := size+next > plan.maxChars || int64(len(command)+len(current)+1) > fieldLimit
		if !full && len(current) > 0 && tooLarge && count > 0 && plan.exact {
			return nil, nativeutility.LimitExceeded("argv_bytes", plan.maxChars, size+next)
		}
		if full || (len(current) > 0 && tooLarge) {
			flush()
		}
		if size+next > plan.maxChars || int64(len(command)+1) > fieldLimit {
			return nil, nativeutility.LimitExceeded("argv_bytes", plan.maxChars, size+next)
		}
		current = append(current, commandprogram.Field{Value: item, Span: span})
		size += next
	}
	flush()
	return out, nil
}

// batchRun is one launched batch and its captured streams.
type batchRun struct {
	argv     commandprogram.Fields
	out, err *meteredBuffer
	result   toolcontract.ProducerResult
	failure  error
	done     chan struct{}
}

// execute runs batches with at most parallel in flight and delivers each
// batch's output and stderr in input order. Statuses follow GNU xargs: a
// batch status 1-125 makes xargs 123; 255 stops later batches with 124; 126
// and 127 stop with that status. A host stop ends the run with that batch's
// outcome once batches already launched finish; their output is discarded.
func (x xargsRun) execute(batches []commandprogram.Fields, parallel int) error {
	inv := x.inv
	if parallel == 1 {
		for _, argv := range batches {
			run := &batchRun{argv: argv}
			x.trace(inv.streams.Error, argv)
			run.result, run.failure = inv.ports.Nested.RunNested(inv.ctx, inv.request, argv, x.environment(), commandprogram.Streams{Input: commandprogram.NullInput(), Output: inv.out, Error: inv.streams.Error})
			if stop, err := x.settle(run); stop || err != nil {
				return err
			}
		}
		return nil
	}
	runs := make([]*batchRun, 0, len(batches))
	var wg sync.WaitGroup
	defer wg.Wait()
	launch := func() {
		run := &batchRun{argv: batches[len(runs)], out: newMeteredBuffer(inv), err: newMeteredBuffer(inv), done: make(chan struct{})}
		runs = append(runs, run)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(run.done)
			x.trace(run.err, run.argv)
			run.result, run.failure = inv.ports.Nested.RunNested(inv.ctx, inv.request, run.argv, x.environment(), commandprogram.Streams{Input: commandprogram.NullInput(), Output: run.out, Error: run.err})
		}()
	}
	for delivered := 0; delivered < len(batches); delivered++ {
		for len(runs) < len(batches) && len(runs)-delivered < parallel {
			launch()
		}
		run := runs[delivered]
		<-run.done
		stop, err := x.settle(run)
		if err == nil {
			err = x.deliver(run)
		}
		run.out.release()
		run.err.release()
		if stop || err != nil {
			wg.Wait()
			for _, later := range runs[delivered+1:] {
				later.out.release()
				later.err.release()
			}
			return err
		}
	}
	return nil
}

func (x xargsRun) deliver(run *batchRun) error {
	if _, err := x.inv.out.Write(run.out.Bytes()); err != nil {
		return err
	}
	_, err := x.inv.streams.Error.Write(run.err.Bytes())
	return err
}

// settle applies one batch outcome; stop reports that no later batch runs.
func (x xargsRun) settle(run *batchRun) (bool, error) {
	result := run.result
	if run.failure != nil || result.Terminal != toolcontract.ProducerCompleted || result.StopCode != "" {
		return true, &batchStop{result: result, cause: run.failure}
	}
	switch status := result.Status; {
	case status == 255:
		x.inv.status = 124
		return true, nil
	case status == 126 || status == 127:
		x.inv.status = status
		return true, nil
	case status != 0:
		x.inv.status = max(x.inv.status, 123)
	}
	return false, nil
}

// trace writes the batch command line to stderr under -t.
func (x xargsRun) trace(w io.Writer, argv commandprogram.Fields) {
	if x.inv.flag("-t") {
		_, _ = io.WriteString(w, strings.Join(argv.Values(), " ")+"\n")
	}
}

func (x xargsRun) environment() map[string]string {
	environment := make(map[string]string, len(x.inv.request.Environment))
	for _, entry := range x.inv.request.Environment {
		name, value, _ := strings.Cut(entry, "=")
		environment[name] = value
	}
	return environment
}

// meteredBuffer captures one parallel batch stream under stream_bytes,
// charging program memory as it grows.
type meteredBuffer struct {
	ctx      context.Context
	memory   *toolcontract.ProgramMemory
	maximum  int64
	buffer   bytes.Buffer
	reserved int64
	releases []func()
}

func newMeteredBuffer(inv *invocation) *meteredBuffer {
	return &meteredBuffer{ctx: inv.ctx, memory: inv.ports.Memory, maximum: inv.limit("stream_bytes")}
}

func (b *meteredBuffer) Write(p []byte) (int, error) {
	size := int64(b.buffer.Len() + len(p))
	if size > b.maximum {
		return 0, nativeutility.LimitExceeded("stream_bytes", b.maximum, size)
	}
	if size > b.reserved {
		grow := max(size-b.reserved, 32<<10)
		release, err := b.memory.Reserve(b.ctx, grow)
		if err != nil {
			return 0, err
		}
		b.releases = append(b.releases, release)
		b.reserved += grow
	}
	return b.buffer.Write(p)
}

func (b *meteredBuffer) Bytes() []byte { return b.buffer.Bytes() }

func (b *meteredBuffer) release() {
	for _, release := range b.releases {
		release()
	}
	b.releases = nil
}
