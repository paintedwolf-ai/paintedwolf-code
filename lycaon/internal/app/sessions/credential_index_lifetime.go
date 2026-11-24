package sessions

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

// begin serializes evidence transitions while allowing waiting providers,
// tools, and observers to cancel. Every admitted operation joins shutdown.
func (i *credentialIndex) begin(ctx context.Context) (context.Context, func(), error) {
	ctx, finish, err := i.work.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	waitStart := time.Now()
	release, err := i.access.Acquire(ctx, "evidence")
	waitMS := time.Since(waitStart).Milliseconds()
	if err != nil {
		secretmatch.EmitPhaseRecord(ctx, secretmatch.ScreenPhaseRecord{
			CallID: secretmatch.ScreenCallIDFrom(ctx), Phase: "root_readiness_wait",
			DurationMS: waitMS, Failed: true,
		})
		finish()
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		secretmatch.EmitPhaseRecord(ctx, secretmatch.ScreenPhaseRecord{
			CallID: secretmatch.ScreenCallIDFrom(ctx), Phase: "root_readiness_wait",
			DurationMS: waitMS, Failed: true,
		})
		release()
		finish()
		return nil, nil, err
	}
	secretmatch.EmitPhaseRecord(ctx, secretmatch.ScreenPhaseRecord{
		CallID: secretmatch.ScreenCallIDFrom(ctx), Phase: "root_readiness_wait",
		DurationMS: waitMS,
	})
	return ctx, func() {
		release()
		finish()
	}, nil
}
