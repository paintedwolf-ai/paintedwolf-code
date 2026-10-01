// Package tsparse bounds source parsing and preserves failure diagnostics.
package tsparse

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/odvcencio/gotreesitter"
)

// Parse returns a complete tree, including syntax faults, or a Failure.
func Parse(ctx context.Context, lang *gotreesitter.Language, src []byte, purpose Purpose) (*gotreesitter.Tree, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, newFailure(lang, src, "configuration", 0, err)
	}
	timeout, err := cfg.timeout(purpose)
	if err != nil {
		return nil, newFailure(lang, src, "configuration", 0, err)
	}
	return ParseWithin(ctx, lang, src, timeout)
}

// ParseWithin accepts an explicit positive timeout for bounded diagnostics.
func ParseWithin(ctx context.Context, lang *gotreesitter.Language, src []byte, timeout time.Duration) (tree *gotreesitter.Tree, err error) {
	start := time.Now()
	failure := newFailure(lang, src, "", timeout, nil)
	defer func() {
		if recovered := recover(); recovered != nil {
			failure.Reason = "panic"
			failure.Cause = fmt.Errorf("parser panic: %v", recovered)
			err = failure
		}
		if failure.Cause != nil {
			failure.Detail = failure.Cause.Error()
		}
		failure.ElapsedMS = time.Since(start).Milliseconds()
		if err != nil && tree != nil {
			tree.Release()
			tree = nil
		}
	}()
	if timeout <= 0 {
		failure.Reason = "configuration"
		failure.Cause = fmt.Errorf("source parsing timeout must be positive")
		return nil, failure
	}
	if lang == nil {
		failure.Reason = "unavailable"
		failure.Cause = fmt.Errorf("source grammar is unavailable")
		return nil, failure
	}
	if ctx.Err() != nil {
		failure.Reason, failure.Cause = contextStop(ctx)
		return nil, failure
	}
	p := gotreesitter.NewParser(lang)
	// Grammar tables are shared initialization, outside the per-source budget.
	lang.LexAsciiTable()
	lang.KeywordLexAsciiTable()
	lang.LexModeStarts()
	p.SetTimeoutMicros(uint64(max(1, timeout.Microseconds())))
	var canceled uint32
	p.SetCancellationFlag(&canceled)
	stop := context.AfterFunc(ctx, func() { atomic.StoreUint32(&canceled, 1) })
	defer stop()
	tree, err = p.Parse(src)
	if err != nil {
		failure.Reason, failure.Cause = "parser_error", err
		var stopped *gotreesitter.ParseStoppedEarlyError
		if errors.As(err, &stopped) {
			failure.Reason = string(stopped.Reason)
			failure.captureRuntime(stopped.Runtime)
		}
		if ctx.Err() != nil {
			failure.Reason, failure.Cause = contextStop(ctx)
		}
		return tree, failure
	}
	if tree == nil {
		failure.Reason = "no_tree"
		return nil, failure
	}
	runtime := tree.ParseRuntime()
	failure.ParsedBytes = int(runtime.LastTokenEndByte)
	if ctx.Err() != nil {
		failure.Reason, failure.Cause = contextStop(ctx)
		return tree, failure
	}
	if tree.ParseStoppedEarly() {
		failure.Reason = string(tree.ParseStopReason())
		failure.captureRuntime(runtime)
		return tree, failure
	}
	return tree, nil
}

func contextStop(ctx context.Context) (string, error) {
	cause := context.Cause(ctx)
	if !errors.Is(cause, ctx.Err()) {
		cause = errors.Join(ctx.Err(), cause)
	}
	if ctx.Err() == context.DeadlineExceeded {
		return "deadline", cause
	}
	return "canceled", cause
}
