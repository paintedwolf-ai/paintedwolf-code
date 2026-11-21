package bgprocess

import (
	"context"

	"github.com/lycaon/lycaon/internal/hostcmd"
)

func prepareJobStart(ctx, runCtx context.Context, req hostcmd.Request) (hostcmd.SourceIdentity, error) {
	if req.BeforeStart == nil {
		return hostcmd.SourceIdentity{}, nil
	}
	if err := runCtx.Err(); err != nil {
		return hostcmd.SourceIdentity{}, err
	}
	// Preparation belongs to the call and also stops if the job is torn down.
	prepCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(runCtx, cancel)
	defer stop()
	source, err := req.PrepareStart(prepCtx)
	if err != nil {
		return hostcmd.SourceIdentity{}, err
	}
	return source, prepCtx.Err()
}
