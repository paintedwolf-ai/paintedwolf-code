package commandprogram

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/commandsurface"
)

var errReadTimedOut = errors.New("read timer expired")

func readTimeoutDuration(bound BoundArgv) (time.Duration, bool, bool) {
	option, timed := bound.Last("-t")
	if !timed {
		return 0, false, true
	}
	value := option.Values[0].Value
	duration, valid := ParseDuration(value)
	return duration, true, valid && !strings.ContainsAny(value, "smhd")
}

func readRecordWithTimeout(ctx context.Context, f *frame, s commandsurface.Statement, fields Fields, duration time.Duration, timed bool, delim byte, raw bool) ([]byte, []bool, bool, bool, error) {
	if !timed || IsNullInput(f.io.streams.Input) {
		record, escaped, complete, err := f.readRecord(ctx, s.SyntaxLocation, delim, raw)
		return record, escaped, complete, false, err
	}
	until := f.e.clock().Now().Add(duration)
	readCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	restore := f.wait.Deadline(until, strings.Join(fields.Values(), " "))
	defer restore()
	stop := f.e.clock().AfterFunc(duration, func() { cancel(errReadTimedOut) })
	defer stop()
	record, escaped, complete, readErr := f.readRecord(readCtx, s.SyntaxLocation, delim, raw)
	timedOut := errors.Is(context.Cause(readCtx), errReadTimedOut)
	if timedOut {
		readErr = nil
	}
	return record, escaped, complete, timedOut, readErr
}

// contextInput cancels one read without closing its inherited descriptor.
type contextInput interface {
	ReadContext(context.Context, []byte) (int, error)
}

func readContext(ctx context.Context, input io.Reader, b []byte) (int, error) {
	if reader, ok := input.(contextInput); ok {
		return reader.ReadContext(ctx, b)
	}
	return input.Read(b)
}

func inputReady(input io.Reader) bool {
	switch input := input.(type) {
	case interface{ InputReady() bool }:
		return input.InputReady()
	case interface{ Len() int }:
		return true
	case interface{ Stat() (fs.FileInfo, error) }:
		info, err := input.Stat()
		return err == nil && info.Mode().IsRegular()
	default:
		return false
	}
}
