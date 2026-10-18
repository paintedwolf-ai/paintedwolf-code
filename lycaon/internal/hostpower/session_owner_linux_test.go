//go:build linux

package hostpower

import (
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lycaon/lycaon/internal/linuxbus"
	"github.com/lycaon/lycaon/internal/testutil"
)

type providerInhibitor struct{ provider provider }

func (p providerInhibitor) Availability() error     { return availabilityOf([]provider{p.provider}) }
func (p providerInhibitor) Acquire() (lease, error) { return acquireFrom([]provider{p.provider}) }

func servePowerOwner(t *testing.T, path string) (*dbus.Conn, *fixturePowerManagement) {
	t.Helper()
	bus, err := linuxbus.Connect(t.Context(), path)
	testutil.FailErr(t, "connect power owner", err)
	t.Cleanup(func() { _ = bus.Close() })
	fixture := &fixturePowerManagement{}
	testutil.FailErr(t, "export power owner", bus.Export(fixture, powerManagementPath, powerManagementInhibit))
	ownName(t, bus, powerManagementName)
	return bus, fixture
}

func awaitOwnerLoss(t *testing.T, held lease) {
	t.Helper()
	select {
	case err := <-held.Done():
		if err == nil || reasonFor(powerManagementProvider{}, err).Code != ReasonDisconnected {
			t.Fatalf("owner loss result = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("service loss left an inhibition active on a live bus")
	}
}

func TestPowerOwnerReplacementSettlesConcurrentLeasesAndRecoversWithoutReusingCookies(t *testing.T) {
	path := fixtureBus(t)
	previous, old := servePowerOwner(t, path)
	subject := powerManagementProvider{connect: connectTo(path)}
	first, err := subject.Acquire(t.Context())
	testutil.FailErr(t, "first inhibition", err)
	defer func() { _ = first.Release() }()
	second, err := subject.Acquire(t.Context())
	testutil.FailErr(t, "second inhibition", err)
	defer func() { _ = second.Release() }()
	controller := newController(true, providerInhibitor{provider: subject})
	controller.retryDelay = time.Millisecond
	defer func() { _ = controller.Close() }()
	controller.SetActive("work-one", true)
	controller.SetActive("work-two", true)
	if status := controller.Snapshot(); !status.Inhibiting {
		t.Fatalf("initial controller status = %+v", status)
	}
	_, err = previous.ReleaseName(powerManagementName)
	testutil.FailErr(t, "retire previous owner", err)
	_, current := servePowerOwner(t, path)
	awaitOwnerLoss(t, first)
	awaitOwnerLoss(t, second)
	testutil.FailErr(t, "release first old cookie", first.Release())
	testutil.FailErr(t, "release second old cookie", second.Release())
	deadline := time.Now().Add(2 * time.Second)
	for {
		current.mu.Lock()
		acquired := current.application != ""
		current.mu.Unlock()
		if acquired && controller.Snapshot().Inhibiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("replacement provider was not acquired: %+v", controller.Snapshot())
		}
		time.Sleep(time.Millisecond)
	}
	old.mu.Lock()
	oldReleases := len(old.released)
	old.mu.Unlock()
	current.mu.Lock()
	newReleases := len(current.released)
	current.mu.Unlock()
	if oldReleases != 3 || newReleases != 0 {
		t.Fatalf("release counts old=%d current=%d, want 3 and 0", oldReleases, newReleases)
	}
	controller.SetActive("work-one", false)
	if status := controller.Snapshot(); !status.Inhibiting || status.ActiveWorkCount != 1 {
		t.Fatalf("overlapping work lost its replacement inhibition: %+v", status)
	}
	testutil.FailErr(t, "close recovered controller", controller.Close())
	current.mu.Lock()
	defer current.mu.Unlock()
	if len(current.released) != 1 {
		t.Fatalf("replacement release count = %d", len(current.released))
	}
}

func TestPortalOwnerReplacementCannotSettleOrReceiveAnotherOwnersRequest(t *testing.T) {
	path := fixtureBus(t)
	previous := servePortal(t, path, false)
	subject := portalProvider{connect: connectTo(path), appID: testAppID, unitAppID: inOwnUnit}
	held, err := subject.Acquire(t.Context())
	testutil.FailErr(t, "portal inhibition", err)
	defer func() { _ = held.Release() }()
	previous.mu.Lock()
	request := previous.request
	previous.mu.Unlock()
	other, err := linuxbus.Connect(t.Context(), path)
	testutil.FailErr(t, "connect foreign signal source", err)
	defer func() { _ = other.Close() }()
	testutil.FailErr(t, "emit foreign response", other.Emit(request, portalRequest+".Response", uint32(2), map[string]dbus.Variant{}))
	select {
	case err := <-held.Done():
		t.Fatalf("foreign response settled the request: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	_, err = previous.server.ReleaseName(portalName)
	testutil.FailErr(t, "retire portal owner", err)
	current := servePortal(t, path, false)
	awaitOwnerLoss(t, held)
	testutil.FailErr(t, "release previous request", held.Release())
	select {
	case <-previous.closed:
	default:
		t.Fatal("previous request was not explicitly closed")
	}
	select {
	case <-current.closed:
		t.Fatal("a replacement portal received the previous request's cleanup")
	default:
	}
}

func TestOwnerExitLeavesReleaseSafeOnTheLiveBus(t *testing.T) {
	path := fixtureBus(t)
	owner, _ := servePowerOwner(t, path)
	held, err := powerManagementProvider{connect: connectTo(path)}.Acquire(t.Context())
	testutil.FailErr(t, "inhibition before service exit", err)
	testutil.FailErr(t, "close service connection", owner.Close())
	awaitOwnerLoss(t, held)
	testutil.FailErr(t, "release after service exit", held.Release())
}
