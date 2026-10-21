//go:build linux

package hostpower

import (
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lycaon/lycaon/internal/testutil"
)

type staticInhibitor struct{ held lease }

func (staticInhibitor) Availability() error        { return nil }
func (s *staticInhibitor) Acquire() (lease, error) { return s.held, nil }

// descriptorsClosed reports whether every inhibitor descriptor the login
// manager fixture handed out has been closed, once the fixture drops its own.
func descriptorsClosed(t *testing.T, fixture *fixtureLogind, reader *os.File, within time.Duration) bool {
	t.Helper()
	fixture.mu.Lock()
	_ = fixture.writer.Close()
	fixture.mu.Unlock()
	ended := make(chan error, 1)
	go func() { var data [1]byte; _, err := reader.Read(data[:]); ended <- err }()
	select {
	case err := <-ended:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("inhibitor pipe = %v", err)
		}
		return true
	case <-time.After(within):
		return false
	}
}

func modes(calls [][]string) []string {
	out := make([]string, 0, len(calls))
	for _, call := range calls {
		out = append(out, call[3])
	}
	return out
}

func TestLegacyLoginManagerHoldsTheSessionSuspendLayerToo(t *testing.T) {
	path := fixtureBus(t)
	logind, reader := serveLogind(t, path, true)
	portal := servePortal(t, path, false)
	layers := layersOn(path)
	testutil.FailErr(t, "availability with a session layer", layers.availability())
	held, err := layers.acquire()
	testutil.FailErr(t, "acquire both layers", err)
	if held.Provider() != "login1+portal" {
		t.Fatalf("provider = %q", held.Provider())
	}
	portal.mu.Lock()
	flags := portal.flags
	portal.mu.Unlock()
	if flags != portalFlagSuspend || !slices.Equal(modes(logind.recorded()), []string{"block"}) {
		t.Fatalf("portal flags %d, login manager modes %v", flags, modes(logind.recorded()))
	}
	controller := newController(true, &staticInhibitor{held: held})
	controller.SetActive("work", true)
	if status := controller.Snapshot(); !status.Inhibiting || status.Provider != "login1+portal" {
		t.Fatalf("status = %+v", status)
	}
	if descriptorsClosed(t, logind, reader, 50*time.Millisecond) {
		t.Fatal("system layer ended while the assertion was held")
	}
	testutil.FailErr(t, "release both layers", controller.Close())
	select {
	case <-portal.closed:
	case <-time.After(time.Second):
		t.Fatal("release left the session layer open")
	}
	if !descriptorsClosed(t, logind, reader, time.Second) {
		t.Fatal("release left the system layer open")
	}
}

func TestLegacyLoginManagerAloneCannotKeepTheDesktopFromSuspending(t *testing.T) {
	path := fixtureBus(t)
	logind, reader := serveLogind(t, path, true)
	layers := layersOn(path)
	var unavailableErr *UnavailableError
	if err := layers.availability(); !errors.As(err, &unavailableErr) {
		t.Fatalf("availability = %v", err)
	}
	codes := []ProviderReasonCode{}
	for _, reason := range unavailableErr.Reasons {
		codes = append(codes, reason.Code)
	}
	if !slices.Equal(codes, []ProviderReasonCode{ReasonNotServed, ReasonOwnUserOverride}) {
		t.Fatalf("reasons = %+v", unavailableErr.Reasons)
	}
	held, err := layers.acquire()
	if held != nil || err == nil || !strings.Contains(err.Error(), "override sleep locks") {
		t.Fatalf("legacy login manager alone lease=%v error=%v", held, err)
	}
	if !descriptorsClosed(t, logind, reader, time.Second) {
		t.Fatal("refused assertion left a sleep lock behind")
	}
}

func TestBindingLoginManagerHoldsAloneAndProbesOncePerInstance(t *testing.T) {
	path := fixtureBus(t)
	logind, reader := serveLogind(t, path, false)
	layers := layersOn(path)
	testutil.FailErr(t, "first availability", layers.availability())
	testutil.FailErr(t, "second availability", layers.availability())
	held, err := layers.acquire()
	testutil.FailErr(t, "acquire the system layer", err)
	if held.Provider() != "login1" {
		t.Fatalf("provider = %q", held.Provider())
	}
	if got := modes(logind.recorded()); !slices.Equal(got, []string{"block-weak", "block"}) {
		t.Fatalf("login manager modes = %v", got)
	}
	testutil.FailErr(t, "release", held.Release())
	if !descriptorsClosed(t, logind, reader, time.Second) {
		t.Fatal("mode probe or lease left a sleep lock behind")
	}
}

func TestRefusedSessionLayerFallsBackOnlyToABindingLoginManager(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		path := fixtureBus(t)
		logind, reader := serveLogind(t, path, legacy)
		servePortal(t, path, true)
		held, err := layersOn(path).acquire()
		if legacy {
			if held != nil || err == nil || !strings.Contains(err.Error(), "inhibit denied") {
				t.Fatalf("legacy with refused portal lease=%v error=%v", held, err)
			}
			if !descriptorsClosed(t, logind, reader, time.Second) {
				t.Fatal("refused assertion left a sleep lock behind")
			}
			continue
		}
		testutil.FailErr(t, "binding login manager with refused portal", err)
		if held.Provider() != "login1" {
			t.Fatalf("provider = %q", held.Provider())
		}
		testutil.FailErr(t, "release", held.Release())
	}
}

func TestLosingOneLayerReleasesTheWholeAssertion(t *testing.T) {
	path := fixtureBus(t)
	logind, reader := serveLogind(t, path, true)
	portal := servePortal(t, path, false)
	held, err := layersOn(path).acquire()
	testutil.FailErr(t, "acquire both layers", err)
	portal.mu.Lock()
	request := portal.request
	portal.mu.Unlock()
	testutil.FailErr(t, "end the session layer", portal.server.Emit(request, portalRequest+".Response", uint32(2), map[string]dbus.Variant{}))
	select {
	case err := <-held.Done():
		if err == nil || !strings.HasPrefix(err.Error(), "portal: ") {
			t.Fatalf("layer loss = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session layer loss did not end the assertion")
	}
	if !descriptorsClosed(t, logind, reader, time.Second) {
		t.Fatal("session layer loss left the system layer held")
	}
	testutil.FailErr(t, "release after loss", held.Release())
}
