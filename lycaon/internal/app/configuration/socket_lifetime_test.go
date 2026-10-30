//go:build linux || darwin

package configuration

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/resourcelifecycle"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testutil"
)

type socketResourceLifetime struct {
	registry *resourcelifecycle.Registry
	order    int
}

func (r *socketResourceLifetime) Track(name string, order int, cleanup func(context.Context) error) {
	r.order = order
	_ = r.registry.Track(resourcelifecycle.DeviceScope(), name, order, func(ctx context.Context, _ resourcelifecycle.Scope) error { return cleanup(ctx) })
}

func TestLoadedSocketStoreClosesAfterConsumerDrainAndOnPartialBuild(t *testing.T) {
	for _, beforeSessions := range []bool{false, true} {
		t.Run(map[bool]string{false: "consumer-retry", true: "before-sessions"}[beforeSessions], func(t *testing.T) {
			root, err := os.MkdirTemp("/tmp", "sl-") //nolint:usetesting // Kernel Unix addresses are bounded.
			testutil.FailErr(t, "socket lifecycle fixture", err)
			defer os.RemoveAll(root)
			t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(root, "state"))
			listener, err := net.Listen("unix", filepath.Join(root, "service"))
			testutil.FailErr(t, "socket lifecycle listener", err)
			defer listener.Close()
			owner, err := socketref.Capture(listener.Addr().String())
			testutil.FailErr(t, "socket lifecycle capture", err)
			binding := owner.Binding()
			resolved, err := filepath.EvalSymlinks(listener.Addr().String())
			testutil.FailErr(t, "socket lifecycle resolved name", err)
			lifetime := &socketResourceLifetime{registry: resourcelifecycle.New()}
			var runtime Runtime
			testutil.FailErr(t, "load actual configuration owner", runtime.Load(Config{}, lifetime))
			store := runtime.Service.Approvals
			created, err := store.UpsertGlobalGrant(settings.ApprovalGrant{ID: "socket-lifetime", Scope: hitl.ApprovalGrantScopeDevice, Category: settings.ApprovalCategorySocketPath, Pattern: listener.Addr().String(), ApprovedPath: listener.Addr().String(), ResolvedPath: resolved, Binding: binding, GrantedByPersonID: testutil.HostOwner().ID, Title: "Allow local service", Coverage: "fixture service", GrantedAt: time.Now().UTC(), ExpiresWhen: "revoked", ReaskWhen: "service changes"})
			testutil.FailErr(t, "install actual configuration lease", err)
			if !created {
				t.Fatal("configuration socket grant not installed")
			}
			owner.Close()
			if lifetime.order <= 25 {
				t.Fatalf("socket store closes before checkpoint drain: %d", lifetime.order)
			}
			if !beforeSessions {
				drained := false
				testutil.FailErr(t, "register actual prior drain boundary", lifetime.registry.Track(resourcelifecycle.DeviceScope(), "checkpoint-expiries", 25, func(context.Context, resourcelifecycle.Scope) error {
					if !drained {
						return context.Canceled
					}
					return nil
				}))
				if err := lifetime.registry.Dispose(t.Context(), resourcelifecycle.DeviceScope()); !errors.Is(err, context.Canceled) {
					t.Fatalf("consumer drain = %v", err)
				}
				live, err := binding.Acquire()
				testutil.FailErr(t, "undrained consumer retains store authority", err)
				live.Close()
				drained = true
			}
			testutil.FailErr(t, "dispose actual configuration registration", lifetime.registry.Dispose(t.Context(), resourcelifecycle.DeviceScope()))
			if live, err := binding.Acquire(); !errors.Is(err, socketref.ErrUnbound) {
				if live != nil {
					live.Close()
				}
				t.Fatalf("configuration teardown retained binding: %v", err)
			}
			rows := store.GlobalGrants()
			if len(rows) != 1 || !rows[0].Unavailable {
				t.Fatal("configuration shutdown discarded intent or kept it usable")
			}
			testutil.FailErr(t, "idempotent disposal", lifetime.registry.Dispose(t.Context(), resourcelifecycle.DeviceScope()))
		})
	}
}
