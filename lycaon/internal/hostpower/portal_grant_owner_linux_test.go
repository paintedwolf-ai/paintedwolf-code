//go:build linux

package hostpower

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lycaon/lycaon/internal/linuxbus"
	"github.com/lycaon/lycaon/internal/testutil"
)

type portalAcquisition struct {
	held lease
	err  error
}

func pendingPortalRequest(t *testing.T, fixture *fixturePortal) dbus.ObjectPath {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		fixture.mu.Lock()
		request := fixture.request
		fixture.mu.Unlock()
		if request != "" {
			return request
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("portal did not receive the inhibition request")
	return ""
}

func TestPortalGrantRefusesMalformedTimeoutAndOwnerLossWithoutLeakingRequest(t *testing.T) {
	for _, mode := range []string{"malformed", "timeout", "no-caller-deadline", "owner-loss"} {
		t.Run(mode, func(t *testing.T) {
			path := fixtureBus(t)
			fixture := servePortal(t, path, false)
			fixture.mu.Lock()
			fixture.withhold = mode != "malformed"
			if mode == "malformed" {
				fixture.response = []any{"zero", map[string]dbus.Variant{}}
			}
			fixture.mu.Unlock()
			ctx := t.Context()
			if mode != "no-caller-deadline" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 150*time.Millisecond)
				defer cancel()
			}
			results := make(chan portalAcquisition, 1)
			go func() {
				held, err := (portalProvider{connect: connectTo(path), appID: testAppID, unitAppID: inOwnUnit}).Acquire(ctx)
				results <- portalAcquisition{held, err}
			}()
			_ = pendingPortalRequest(t, fixture)
			var replacement *fixturePortal
			if mode == "owner-loss" {
				_, err := fixture.server.ReleaseName(portalName)
				testutil.FailErr(t, "retire ungranted portal owner", err)
				replacement = servePortal(t, path, false)
			}
			select {
			case result := <-results:
				if result.held != nil || result.err == nil {
					t.Fatalf("ungranted inhibition = %+v", result)
				}
				if mode == "malformed" && !strings.Contains(result.err.Error(), "unknown shape") {
					t.Fatalf("malformed grant result = %v", result.err)
				}
				if (mode == "timeout" || mode == "no-caller-deadline") && !errors.Is(result.err, context.DeadlineExceeded) {
					t.Fatalf("bounded grant result = %v", result.err)
				}
				if mode == "owner-loss" && reasonFor(portalProvider{}, result.err).Code != ReasonDisconnected {
					t.Fatalf("owner loss result = %v", result.err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("ungranted inhibition did not settle")
			}
			select {
			case <-fixture.closed:
			case <-time.After(time.Second):
				t.Fatal("ungranted request was not closed on its original owner")
			}
			fixture.mu.Lock()
			releases := fixture.releases
			fixture.mu.Unlock()
			if releases != 1 {
				t.Fatalf("original owner's cleanup count = %d, want one", releases)
			}
			if replacement != nil {
				select {
				case <-replacement.closed:
					t.Fatal("replacement owner received another owner's cleanup")
				default:
				}
			}
		})
	}
}

func TestForeignPortalResponseCannotGrantTheOwnedRequest(t *testing.T) {
	path := fixtureBus(t)
	fixture := servePortal(t, path, false)
	fixture.mu.Lock()
	fixture.withhold = true
	fixture.mu.Unlock()
	results := make(chan portalAcquisition, 1)
	go func() {
		held, err := (portalProvider{connect: connectTo(path), appID: testAppID, unitAppID: inOwnUnit}).Acquire(t.Context())
		results <- portalAcquisition{held, err}
	}()
	request := pendingPortalRequest(t, fixture)
	foreign, err := linuxbus.Connect(t.Context(), path)
	testutil.FailErr(t, "connect foreign portal response source", err)
	defer func() { _ = foreign.Close() }()
	testutil.FailErr(t, "emit foreign granting response", foreign.Emit(request, portalRequest+".Response", uint32(0), map[string]dbus.Variant{}))
	select {
	case result := <-results:
		t.Fatalf("foreign response completed the inhibition: %+v", result)
	case <-time.After(20 * time.Millisecond):
	}
	testutil.FailErr(t, "emit original owner's granting response", fixture.server.Emit(request, portalRequest+".Response", uint32(0), map[string]dbus.Variant{}))
	select {
	case result := <-results:
		testutil.FailErr(t, "owned portal grant", result.err)
		if result.held == nil {
			t.Fatal("owned grant returned no lease")
		}
		testutil.FailErr(t, "close granted owned request", result.held.Release())
	case <-time.After(time.Second):
		t.Fatal("owned grant did not complete the inhibition")
	}
}

func TestPortalResponseRequiresTheDeclaredCodeAndDetails(t *testing.T) {
	request := dbus.ObjectPath("/request/owned")
	for _, body := range [][]any{
		nil,
		{uint32(0)},
		{uint32(0), "details"},
		{uint32(0), map[string]dbus.Variant{}, "extra"},
		{"zero", map[string]dbus.Variant{}},
	} {
		_, matches, err := responseCode(&dbus.Signal{Path: request, Name: portalRequest + ".Response", Body: body}, request)
		if !matches || err == nil {
			t.Fatalf("malformed portal body %#v: matches=%t error=%v", body, matches, err)
		}
	}
	for _, signal := range []*dbus.Signal{
		{Path: "/request/other", Name: portalRequest + ".Response"},
		{Path: request, Name: portalRequest + ".Other"},
	} {
		_, matches, err := responseCode(signal, request)
		if matches || err != nil {
			t.Fatalf("unrelated signal: matches=%t error=%v", matches, err)
		}
	}
}
