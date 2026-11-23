//go:build darwin || linux

package privateguesthost

import (
	"context"
	"errors"
	"io"

	"github.com/lycaon/lycaon/internal/serviceguest"
)

type handshakeFixture struct{ serviceguest.Executor }

func (handshakeFixture) Start(_ context.Context, id string, _ []string, _ []byte) (serviceguest.Job, error) {
	return serviceguest.Job{ID: id, State: serviceguest.Exited}, nil
}

func (handshakeFixture) Close() error { return nil }
func handshakeFixtureStatus(channel io.ReadWriteCloser) int {
	err := serviceguest.Serve(context.Background(), channel, handshakeFixture{})
	if errors.Is(err, io.EOF) {
		return 0
	}
	return 1
}
