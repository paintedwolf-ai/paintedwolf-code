package wiring

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/app"
	"github.com/lycaon/lycaon/internal/startupprotocol"
	"github.com/lycaon/lycaon/internal/testutil"
)

// ServeStartup observes when an application can accept HTTP requests.
type ServeStartup struct {
	ready chan struct{}
}

func NewServeStartup() *ServeStartup {
	return &ServeStartup{ready: make(chan struct{})}
}

func (*ServeStartup) Phase(startupprotocol.Phase) error { return nil }
func (*ServeStartup) Failed(string) error               { return nil }
func (s *ServeStartup) Ready(int) error {
	close(s.ready)
	return nil
}

// Start runs the built app and joins its shutdown before resource cleanup.
func (s *ServeStartup) Start(t *testing.T, app *app.ServeApp) string {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	exited := make(chan error, 1)
	go func() {
		defer close(exited)
		err := app.Run(ctx)
		if err == nil && ctx.Err() == nil {
			err = errors.New("serve loop exited before shutdown")
		}
		exited <- err
	}()
	t.Cleanup(func() {
		cancel()
		testutil.FailErr(t, "drain served app", <-exited)
	})

	select {
	case <-s.ready:
		return "http://" + app.ListenAddr
	case err := <-exited:
		testutil.FailErr(t, "run served app", err)
		t.Fatal("served app exited before listening")
		return ""
	}
}

func (h *Harness) Start(t *testing.T) string {
	t.Helper()
	if h.settlesTopology {
		t.Fatal("topology settlement fixture requires an isolated queue claimant")
	}
	return h.startup.Start(t, h.ServeApp)
}
