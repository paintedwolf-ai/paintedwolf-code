//go:build linux

package hostpower

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lycaon/lycaon/internal/linuxbus"
	"github.com/lycaon/lycaon/internal/testutil"
)

type recordedTransientUnit struct {
	name       string
	mode       string
	properties []systemdProperty
	aux        []systemdAuxUnit
}

type fixtureSystemdManager struct {
	mu      sync.Mutex
	unit    string
	refuse  bool
	onStart func(string)
	calls   []recordedTransientUnit
}

func serveSystemdManager(t *testing.T, path string, initialUnit string, refuse bool) *fixtureSystemdManager {
	t.Helper()
	server, err := linuxbus.Connect(t.Context(), path)
	testutil.FailErr(t, "connect systemd manager fixture", err)
	t.Cleanup(func() { _ = server.Close() })
	manager := &fixtureSystemdManager{unit: initialUnit, refuse: refuse}
	manager.onStart = func(name string) {
		manager.unit = unitAppID(name)
	}
	testutil.FailErr(t, "export systemd manager fixture", server.Export(manager, systemdObjectPath, systemdManagerInterface))
	reply, err := server.RequestName(systemdBusName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("own %s: reply=%v err=%v", systemdBusName, reply, err)
	}
	return manager
}

func (m *fixtureSystemdManager) StartTransientUnit(
	name string,
	mode string,
	properties []systemdProperty,
	aux []systemdAuxUnit,
) (dbus.ObjectPath, *dbus.Error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, recordedTransientUnit{
		name:       name,
		mode:       mode,
		properties: slices.Clone(properties),
		aux:        slices.Clone(aux),
	})
	if m.refuse {
		return "", dbus.MakeFailedError(errors.New("transient unit denied"))
	}
	if m.onStart != nil {
		m.onStart(name)
	}
	return dbus.ObjectPath("/org/freedesktop/systemd1/job/1"), nil
}

func (m *fixtureSystemdManager) currentUnit() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.unit
}

func (m *fixtureSystemdManager) recorded() []recordedTransientUnit {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.calls)
}

func TestPortalWithoutRegistryEntersApplicationScopeOnceWhenUnscoped(t *testing.T) {
	path := fixtureBus(t)
	logind, reader := serveLogind(t, path, true)
	portal := servePortalWith(t, path, false, nil)
	manager := serveSystemdManager(t, path, "", false)

	layers := sleepLayers{
		system:  login1Provider{connect: connectTo(path), semantics: &lockSemantics{}},
		session: portalProvider{connect: connectTo(path), appID: testAppID, unitAppID: manager.currentUnit},
	}
	testutil.FailErr(t, "availability with unscoped process", layers.availability())
	held, err := layers.acquire()
	testutil.FailErr(t, "acquire both layers after scope migration", err)
	if held.Provider() != "login1+portal" {
		t.Fatalf("provider = %q", held.Provider())
	}

	calls := manager.recorded()
	if len(calls) != 1 {
		t.Fatalf("StartTransientUnit calls = %d, want 1", len(calls))
	}
	wantUnit := fmt.Sprintf("app-paintedwolf-%s-%d.scope", testAppID, os.Getpid())
	if calls[0].name != wantUnit || calls[0].mode != "fail" || len(calls[0].aux) != 0 {
		t.Fatalf("StartTransientUnit = %+v, want unit %q mode fail", calls[0], wantUnit)
	}
	if len(calls[0].properties) != 1 || calls[0].properties[0].Name != "PIDs" {
		t.Fatalf("StartTransientUnit properties = %+v", calls[0].properties)
	}
	pids, ok := calls[0].properties[0].Value.Value().([]uint32)
	if !ok || !slices.Equal(pids, []uint32{uint32(os.Getpid())}) {
		t.Fatalf("StartTransientUnit PIDs = %#v", calls[0].properties[0].Value.Value())
	}

	testutil.FailErr(t, "release both layers", held.Release())
	select {
	case <-portal.closed:
	case <-time.After(time.Second):
		t.Fatal("release left the session layer open")
	}
	if !descriptorsClosed(t, logind, reader) {
		t.Fatal("release left the system layer open")
	}
}

func TestPortalWithoutRegistrySkipsTransientScopeWhenAlreadyScoped(t *testing.T) {
	path := fixtureBus(t)
	servePortalWith(t, path, false, nil)
	manager := serveSystemdManager(t, path, testAppID, false)
	provider := portalProvider{connect: connectTo(path), appID: testAppID, unitAppID: manager.currentUnit}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	testutil.FailErr(t, "check already-scoped portal", provider.Check(ctx))
	held, err := provider.Acquire(ctx)
	testutil.FailErr(t, "acquire already-scoped portal", err)
	testutil.FailErr(t, "release already-scoped portal", held.Release())

	if calls := manager.recorded(); len(calls) != 0 {
		t.Fatalf("StartTransientUnit calls = %+v, want none", calls)
	}
}

func TestPortalWithoutRegistryConcurrentAttributionEntersScopeOnce(t *testing.T) {
	path := fixtureBus(t)
	servePortalWith(t, path, false, nil)
	manager := serveSystemdManager(t, path, "", false)
	provider := portalProvider{connect: connectTo(path), appID: testAppID, unitAppID: manager.currentUnit}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := make(chan struct{})
	const workers = 6
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- provider.Check(ctx)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		testutil.FailErr(t, "concurrent portal attribution", err)
	}
	if calls := manager.recorded(); len(calls) != 1 {
		t.Fatalf("concurrent StartTransientUnit calls = %d, want 1", len(calls))
	}
}

func TestPortalWithoutRegistryRefusesForeignScopeAndTransientUnitFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	t.Run("foreign application unit is not migrated", func(t *testing.T) {
		path := fixtureBus(t)
		servePortalWith(t, path, false, nil)
		manager := serveSystemdManager(t, path, "com.visualstudio.code", false)
		provider := portalProvider{connect: connectTo(path), appID: testAppID, unitAppID: manager.currentUnit}

		err := provider.Check(ctx)
		if reasonFor(provider, err).Code != ReasonNoApplicationID || !strings.Contains(err.Error(), "com.visualstudio.code") {
			t.Fatalf("foreign unit check = %v", err)
		}
		if _, err := provider.Acquire(ctx); reasonFor(provider, err).Code != ReasonNoApplicationID {
			t.Fatalf("foreign unit acquire = %v", err)
		}
		if calls := manager.recorded(); len(calls) != 0 {
			t.Fatalf("foreign unit StartTransientUnit calls = %+v, want none", calls)
		}
	})

	t.Run("transient unit error refuses without application id", func(t *testing.T) {
		path := fixtureBus(t)
		servePortalWith(t, path, false, nil)
		manager := serveSystemdManager(t, path, "", true)
		provider := portalProvider{connect: connectTo(path), appID: testAppID, unitAppID: manager.currentUnit}

		err := provider.Check(ctx)
		if reasonFor(provider, err).Code != ReasonNoApplicationID || !strings.Contains(err.Error(), "outside an application unit") {
			t.Fatalf("refused transient unit check = %v", err)
		}
		if _, err := provider.Acquire(ctx); reasonFor(provider, err).Code != ReasonNoApplicationID {
			t.Fatalf("refused transient unit acquire = %v", err)
		}
		if calls := manager.recorded(); len(calls) != 2 {
			t.Fatalf("refused transient unit calls = %d, want 2", len(calls))
		}
	})
}
