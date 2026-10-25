//go:build linux

package hostpower

import (
	"context"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lycaon/lycaon/internal/linuxbus"
	"github.com/lycaon/lycaon/internal/testutil"
)

type providerInhibitor struct{ provider provider }

func (p providerInhibitor) Availability() error {
	ctx, cancel := context.WithTimeout(context.Background(), busDeadline)
	defer cancel()
	return p.provider.Check(ctx)
}

func (p providerInhibitor) Acquire() (lease, error) {
	ctx, cancel := context.WithTimeout(context.Background(), busDeadline)
	defer cancel()
	return p.provider.Acquire(ctx)
}

func awaitOwnerLoss(t *testing.T, held lease) {
	t.Helper()
	select {
	case err := <-held.Done():
		if err == nil || reasonFor(portalProvider{}, err).Code != ReasonDisconnected {
			t.Fatalf("owner loss result = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("service loss left an inhibition active on a live bus")
	}
}

func TestPortalOwnerReplacementSettlesConcurrentLeasesAndRecoversWithoutReusingRequests(t *testing.T) {
	path := fixtureBus(t)
	old := servePortal(t, path, false)
	subject := portalProvider{connect: connectTo(path), appID: testAppID, unitAppID: inOwnUnit}
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
	_, err = old.server.ReleaseName(portalName)
	testutil.FailErr(t, "retire previous owner", err)
	current := servePortal(t, path, false)
	awaitOwnerLoss(t, first)
	awaitOwnerLoss(t, second)
	testutil.FailErr(t, "release first old request", first.Release())
	testutil.FailErr(t, "release second old request", second.Release())
	deadline := time.Now().Add(2 * time.Second)
	for {
		current.mu.Lock()
		acquired := current.request != ""
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
	oldReleases := old.releases
	old.mu.Unlock()
	current.mu.Lock()
	newReleases := current.releases
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
	if current.releases != 1 {
		t.Fatalf("replacement release count = %d", current.releases)
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
	owner := servePortal(t, path, false)
	held, err := (portalProvider{connect: connectTo(path), appID: testAppID, unitAppID: inOwnUnit}).Acquire(t.Context())
	testutil.FailErr(t, "inhibition before service exit", err)
	testutil.FailErr(t, "close service connection", owner.server.Close())
	awaitOwnerLoss(t, held)
	testutil.FailErr(t, "release after service exit", held.Release())
}
