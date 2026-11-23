package git

import (
	"context"
	"errors"
)

// ErrExpectedHeadMismatch reports that HEAD or status changed after a preview was captured.
var ErrExpectedHeadMismatch = errors.New("git checkout state changed since preview")

type lockedVerifyKey struct{}

// WithLockedVerify attaches a check executed immediately after acquiring the repository lease.
func WithLockedVerify(ctx context.Context, fn func(context.Context) error) context.Context {
	if fn == nil {
		return ctx
	}
	prev, _ := ctx.Value(lockedVerifyKey{}).(func(context.Context) error)
	if prev == nil {
		return context.WithValue(ctx, lockedVerifyKey{}, fn)
	}
	return context.WithValue(ctx, lockedVerifyKey{}, func(c context.Context) error {
		if err := prev(c); err != nil {
			return err
		}
		return fn(c)
	})
}

func checkLockedVerify(ctx context.Context) error {
	fn, _ := ctx.Value(lockedVerifyKey{}).(func(context.Context) error)
	if fn == nil {
		return nil
	}
	return fn(ctx)
}
