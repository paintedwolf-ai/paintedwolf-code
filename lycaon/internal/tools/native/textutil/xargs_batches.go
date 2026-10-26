package textutil

import (
	"bytes"
	"context"
	"github.com/lycaon/lycaon/internal/tools"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

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
				return nil, tools.CommandLimitExceeded("argument_bytes", argumentLimit, int64(len(value)))
			}
			size += int64(len(value))
			argv = append(argv, commandprogram.Field{Value: value, Span: word.Span})
		}
		if size > argvLimit {
			return nil, tools.CommandLimitExceeded("argv_bytes", argvLimit, size)
		}
		out = append(out, argv)
	}
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
		x.inv.stoppedBatch = &result
		return true, run.failure
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
		return 0, tools.CommandLimitExceeded("stream_bytes", b.maximum, size)
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
