// Package privateguesthost launches a session's packaged service environment.
package privateguesthost

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/serviceguest"
)

// OpenPrivateGuest returns the session channel after the supervisor handshake.
func OpenPrivateGuest(ctx context.Context) (*serviceguest.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	channel, err := launchService(ctx)
	if err == nil {
		var client *serviceguest.Client
		client, err = serviceguest.Open(ctx, channel)
		if err == nil {
			return client, nil
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, errors.Join(serviceguest.ErrUnavailable, err)
}
