package commandprogram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// readBuiltin reads one record from the frame's input stream, splits it using
// the reached command's IFS, and assigns the target variables.
func (f *frame) readBuiltin(ctx context.Context, s commandsurface.Statement, fields Fields) error {
	spec, ok, err := ResolveCommand(fields)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("read command is missing from the language catalog")
	}
	bound, err := f.e.ports.Binder.Bind(spec, fields)
	if err != nil {
		var argv *ArgvError
		if errors.As(err, &argv) {
			if gap := f.nativeGap(ctx, spec, argv, fields, nil); gap != nil {
				return f.dispatch(ctx, s, fields, nil)
			}
			f.hostRefusal(spec.Name, argv.Word, argv.Expected, argv.Code)
			refusal := f.refuse(s.SyntaxLocation, argv.Code, argv.Expected, err)
			refusal.Command, refusal.Option, refusal.Form = spec.Name, argv.Word, spec.Argv
			return refusal
		}
		return err
	}
	_, raw := bound.Last("-r")
	delim := byte('\n')
	if opt, ok := bound.Last("-d"); ok {
		if len(opt.Values) > 0 && len(opt.Values[0].Value) > 0 {
			delim = opt.Values[0].Value[0]
		} else {
			delim = 0
		}
	}
	operands := bound.Operands()
	names := []string{"REPLY"}
	hasTargets := len(operands) > 0
	if hasTargets {
		names = make([]string, len(operands))
		for i, op := range operands {
			if !VariableName(op.Value) {
				f.hostRefusal("read", op.Value, "a valid variable name", argvOperandInvalid)
				refusal := f.refuse(s.SyntaxLocation, argvOperandInvalid, "a valid variable name", nil)
				refusal.Command, refusal.Option, refusal.Form = "read", op.Value, spec.Argv
				return refusal
			}
			names[i] = op.Value
		}
	}
	duration, timed, valid := readTimeoutDuration(bound)
	return f.inProcess(ctx, s, "read", func(ctx context.Context, _ toolcontract.OperationInvocation) (toolcontract.ProducerResult, error) {
		if !valid {
			option, _ := bound.Last("-t")
			if _, err := fmt.Fprintf(f.io.streams.Error, "read: %s: invalid timeout specification\n", option.Values[0].Value); err != nil {
				return ownerResult(err), err
			}
			return ordinaryResult(1), nil
		}
		if timed && duration == 0 {
			if inputReady(f.io.streams.Input) {
				return ordinaryResult(0), nil
			}
			return ordinaryResult(1), nil
		}
		record, escaped, sawDelim, timedOut, readErr := readRecordWithTimeout(ctx, f, s, fields, duration, timed, delim, raw)
		if readErr != nil {
			return ownerResult(readErr), readErr
		}
		ifs, set := f.Variable("IFS")
		if !set {
			ifs = defaultIFS
		}
		values := splitReadFields(record, escaped, len(names), !hasTargets && raw, ifs)
		for i, name := range names {
			if err := f.state.vars.Assign(ctx, name, values[i]); err != nil {
				return ownerResult(err), f.expansionRefusal(s.SyntaxLocation, err)
			}
		}
		status := 0
		if !sawDelim {
			status = 1
		}
		if timedOut {
			status = 142
		}
		return ordinaryResult(status), nil
	})
}

func (f *frame) readRecord(ctx context.Context, loc commandsurface.SyntaxLocation, delim byte, raw bool) ([]byte, []bool, bool, error) {
	if f.io.streams.Input == nil {
		return nil, nil, false, nil
	}
	limit := f.e.limits["record_bytes"]
	var record []byte
	var escaped []bool
	var b [1]byte
	for {
		if err := ctx.Err(); err != nil {
			return record, escaped, false, err
		}
		n, err := readContext(ctx, f.io.streams.Input, b[:])
		if n > 0 {
			c := b[0]
			if !raw && c == '\\' {
				n2, err2 := readContext(ctx, f.io.streams.Input, b[:])
				if n2 > 0 {
					if b[0] == '\n' {
						continue
					}
					record = append(record, b[0])
					escaped = append(escaped, true)
				} else {
					record = append(record, '\\')
					escaped = append(escaped, false)
				}
				if int64(len(record)) > limit {
					return nil, nil, false, f.refuseLimit(loc, "record_bytes", int64(len(record)))
				}
				if err2 != nil {
					if errors.Is(err2, io.EOF) {
						return record, escaped, false, nil
					}
					return record, escaped, false, err2
				}
				continue
			}
			if c == delim {
				return record, escaped, true, nil
			}
			record = append(record, c)
			escaped = append(escaped, false)
			if int64(len(record)) > limit {
				return nil, nil, false, f.refuseLimit(loc, "record_bytes", int64(len(record)))
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return record, escaped, false, nil
			}
			return record, escaped, false, err
		}
	}
}

func isIFSByte(c byte, ifs string) bool {
	return strings.IndexByte(ifs, c) >= 0
}

func splitReadFields(record []byte, escaped []bool, count int, preserveAll bool, ifs string) []string {
	out := make([]string, count)
	if count == 0 {
		return out
	}
	if preserveAll || ifs == "" {
		out[0] = string(record)
		return out
	}
	i, end := 0, len(record)
	for i < end && !escaped[i] && isIFSByte(record[i], ifs) && isIFSWhitespace(record[i]) {
		i++
	}
	for end > i && !escaped[end-1] && isIFSByte(record[end-1], ifs) && isIFSWhitespace(record[end-1]) {
		end--
	}
	for field := range count {
		if field == count-1 {
			out[field] = string(record[i:end])
			break
		}
		start := i
		for i < end && (escaped[i] || !isIFSByte(record[i], ifs)) {
			i++
		}
		out[field] = string(record[start:i])
		if i >= end {
			break
		}
		if isIFSWhitespace(record[i]) {
			for i < end && !escaped[i] && isIFSByte(record[i], ifs) && isIFSWhitespace(record[i]) {
				i++
			}
			if i < end && !escaped[i] && isIFSByte(record[i], ifs) && !isIFSWhitespace(record[i]) {
				i++
			}
		} else {
			i++
		}
		for i < end && !escaped[i] && isIFSByte(record[i], ifs) && isIFSWhitespace(record[i]) {
			i++
		}
	}
	return out
}

func isIFSWhitespace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' }
