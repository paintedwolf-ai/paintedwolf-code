package project

import (
	"context"
	"time"
)

// SourceProgress reports observed work within the current lifecycle phase.
type SourceProgress struct {
	Phase   string
	Entries int64
	Bytes   int64
}

type sourceProgressKey struct{}

func WithSourceProgress(ctx context.Context, report func(SourceProgress)) context.Context {
	return context.WithValue(ctx, sourceProgressKey{}, report)
}

func reportSourceProgress(ctx context.Context, phase string, entries, bytes int64) {
	if report, ok := ctx.Value(sourceProgressKey{}).(func(SourceProgress)); ok {
		report(SourceProgress{Phase: phase, Entries: entries, Bytes: bytes})
	}
}

type sourceEffectKey struct{}

// WithSourceEffect binds the operation's cancellation-to-commit boundary.
func WithSourceEffect(ctx context.Context, begin func() error) context.Context {
	return context.WithValue(ctx, sourceEffectKey{}, begin)
}

func beginSourceEffect(ctx context.Context) error {
	if begin, ok := ctx.Value(sourceEffectKey{}).(func() error); ok {
		return begin()
	}
	return ctx.Err()
}

// Each phase reports bounded updates, including within a single large file.
type sourceWorkProgress struct {
	ctx            context.Context
	phase          string
	entries, bytes int64
	last           time.Time
}

func newSourceWorkProgress(ctx context.Context, phase string) *sourceWorkProgress {
	p := &sourceWorkProgress{ctx: ctx, phase: phase}
	p.report(true)
	return p
}

func (p *sourceWorkProgress) noteBytes(n int) {
	p.bytes += int64(n)
	p.report(false)
}

func (p *sourceWorkProgress) entry() {
	p.entries++
	p.report(false)
}

func (p *sourceWorkProgress) report(force bool) {
	if force || time.Since(p.last) >= 200*time.Millisecond {
		reportSourceProgress(p.ctx, p.phase, p.entries, p.bytes)
		p.last = time.Now()
	}
}

func (p *sourceWorkProgress) Write(data []byte) (int, error) {
	p.noteBytes(len(data))
	return len(data), p.ctx.Err()
}
