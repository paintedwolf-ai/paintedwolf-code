package textutil

import (
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
)

// runXargs reads all of standard input, splits it into items and runs
// COMMAND (default echo) in serial batches, in input order. Each batch is one
// child through the evaluator's nested-command port, with its own execution
// identity, binding and receipt; no shell runs and no input becomes program
// text. Empty input runs nothing. A batch's ordinary nonzero status makes
// xargs status 1 and later batches still run; a host stop ends the run with
// that batch's outcome.
// xargsRun is one xargs invocation's input splitting and batch planning.
type xargsRun struct{ inv *invocation }

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

func runXargs(inv *invocation) error {
	x := xargsRun{inv: inv}
	count, err := x.batchCount()
	if err != nil {
		return err
	}
	placeholder, replacing := inv.value("-I")
	if replacing && count > 0 {
		return inv.usage(placeholder, "-I or -n, not both")
	}
	if replacing && placeholder.Value == "" {
		return inv.usage(placeholder, "a nonempty placeholder")
	}
	input, err := x.readAll()
	if err != nil {
		return err
	}
	items, err := x.items(input, replacing)
	if err != nil {
		return err
	}
	command := inv.operands()
	if len(command) == 0 {
		command = commandprogram.Fields{{Value: "echo", Span: inv.request.Invocation.Span}}
	}
	batches, err := x.batches(command, items, count, placeholder, replacing)
	if err != nil || len(batches) == 0 {
		return err
	}
	if inv.ports.Nested == nil {
		return unsupported(inv, command[0], "a program runtime that dispatches xargs batches")
	}
	// Each batch runs in the invoking frame's exported environment.
	environment := make(map[string]string, len(inv.request.Environment))
	for _, entry := range inv.request.Environment {
		name, value, _ := strings.Cut(entry, "=")
		environment[name] = value
	}
	for _, argv := range batches {
		result, err := inv.ports.Nested.RunNested(inv.ctx, inv.request, argv, environment, commandprogram.Streams{Input: commandprogram.NullInput(), Output: inv.out, Error: inv.streams.Error})
		if err != nil || result.Terminal != toolcontract.ProducerCompleted || result.StopCode != "" {
			return &batchStop{result: result, cause: err}
		}
		if result.Status != 0 {
			inv.status = 1
		}
	}
	return nil
}

func (x xargsRun) batchCount() (int, error) {
	value, ok := x.inv.value("-n")
	if !ok {
		return 0, nil
	}
	count, err := strconv.Atoi(value.Value)
	if err != nil || count < 1 || strings.HasPrefix(value.Value, "+") {
		return 0, x.inv.usage(value, "a positive COUNT")
	}
	return count, nil
}

// readAll buffers standard input under stream_bytes, charging memory first.
func (x xargsRun) readAll() ([]byte, error) {
	const chunk = 64 << 10
	maximum := x.inv.limit("stream_bytes")
	var buffer bytes.Buffer
	for {
		if err := x.inv.reserve(chunk); err != nil {
			return nil, err
		}
		buffer.Grow(chunk)
		n, err := io.CopyN(&buffer, x.inv.streams.Input, chunk)
		if int64(buffer.Len()) > maximum {
			return nil, nativeutility.LimitExceeded("stream_bytes", maximum, int64(buffer.Len()))
		}
		if errors.Is(err, io.EOF) || (err == nil && n < chunk) {
			return buffer.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// xargsItems splits input. -0 gives exact NUL-delimited items; -I gives one
// item per nonempty line without leading blanks; otherwise blanks and
// newlines separate items, quotes group and a backslash quotes one byte.
func (x xargsRun) items(input []byte, lines bool) ([]string, error) {
	if x.inv.flag("-0") {
		if len(input) == 0 {
			return nil, nil
		}
		items := strings.Split(string(input), "\x00")
		if items[len(items)-1] == "" {
			items = items[:len(items)-1]
		}
		return items, nil
	}
	if lines {
		var items []string
		for _, line := range strings.Split(string(input), "\n") {
			if line = strings.TrimLeft(line, " \t"); line != "" {
				items = append(items, line)
			}
		}
		return items, nil
	}
	var items []string
	var item strings.Builder
	present := false
	quote := byte(0)
	for i := 0; i < len(input); i++ {
		c := input[i]
		switch {
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
			if c == '\n' {
				return nil, x.inv.usage(x.inv.commandWord(), "input with matched quotes on one line")
			}
			item.WriteByte(c)
		case c == '\'' || c == '"':
			quote, present = c, true
		case c == '\\' && i+1 < len(input):
			i++
			item.WriteByte(input[i])
			present = true
		case c == ' ' || c == '\t' || c == '\n':
			if present {
				items = append(items, item.String())
				item.Reset()
				present = false
			}
		default:
			item.WriteByte(c)
			present = true
		}
	}
	if quote != 0 {
		return nil, x.inv.usage(x.inv.commandWord(), "input with matched quotes")
	}
	if present {
		items = append(items, item.String())
	}
	return items, nil
}

// batches builds every argv before any runs, so a refused item or batch
// launches nothing. Without -n a batch takes as many items as argv_bytes and
// expanded_fields admit.
func (x xargsRun) batches(command commandprogram.Fields, items []string, count int, placeholder commandprogram.Field, replacing bool) ([]commandprogram.Fields, error) {
	span := x.inv.request.Invocation.Span
	argumentLimit, argvLimit, fieldLimit := x.inv.limit("argument_bytes"), x.inv.limit("argv_bytes"), x.inv.limit("expanded_fields")
	base := int64(0)
	for _, word := range command {
		base += int64(len(word.Value))
	}
	var out []commandprogram.Fields
	for _, item := range items {
		if int64(len(item)) > argumentLimit {
			return nil, nativeutility.LimitExceeded("argument_bytes", argumentLimit, int64(len(item)))
		}
		if strings.IndexByte(item, 0) >= 0 {
			return nil, x.inv.usage(x.inv.commandWord(), "items without NUL bytes")
		}
	}
	if replacing {
		for _, item := range items {
			argv := commandprogram.Fields{command[0]}
			size := int64(len(command[0].Value))
			for _, word := range command[1:] {
				value := strings.ReplaceAll(word.Value, placeholder.Value, item)
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
		return out, x.childLimit(len(out))
	}
	var current commandprogram.Fields
	size := base
	flush := func() {
		if len(current) > 0 {
			out = append(out, append(append(commandprogram.Fields{}, command...), current...))
			current, size = nil, base
		}
	}
	for _, item := range items {
		full := count > 0 && len(current) == count
		tooLarge := size+int64(len(item)) > argvLimit || int64(len(command)+len(current)+1) > fieldLimit
		if full || (len(current) > 0 && tooLarge) {
			flush()
		}
		if size+int64(len(item)) > argvLimit || int64(len(command)+1) > fieldLimit {
			return nil, nativeutility.LimitExceeded("argv_bytes", argvLimit, size+int64(len(item)))
		}
		current = append(current, commandprogram.Field{Value: item, Span: span})
		size += int64(len(item))
	}
	flush()
	return out, x.childLimit(len(out))
}

func (x xargsRun) childLimit(batches int) error {
	if limit := x.inv.limit("child_executions"); int64(batches) > limit {
		return nativeutility.LimitExceeded("child_executions", limit, int64(batches))
	}
	return nil
}
