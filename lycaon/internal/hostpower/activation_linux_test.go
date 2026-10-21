//go:build linux

package hostpower

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lycaon/lycaon/internal/linuxbus"
	"github.com/lycaon/lycaon/internal/testutil"
)

func activationBus(t *testing.T) (string, string) {
	t.Helper()
	directory := fixtureSocketDirectory(t)
	path := filepath.Join(directory, "bus")
	services := filepath.Join(directory, "services")
	testutil.FailErr(t, "private activation directory", os.Mkdir(services, 0o700))
	self, err := os.Executable()
	testutil.FailErr(t, "activation fixture executable", err)
	marker := filepath.Join(directory, "started")
	service := fmt.Sprintf("[D-BUS Service]\nName=%s\nExec=/usr/bin/env PW_POWER_ACTIVATION_FIXTURE=1 PW_POWER_ACTIVATION_MARKER=%s %s -test.run=^TestPowerActivationFixtureProcess$\n", powerManagementName, marker, self)
	testutil.FailErr(t, "activation service", os.WriteFile(filepath.Join(services, powerManagementName+".service"), []byte(service), 0o600))
	config := fmt.Sprintf(`<busconfig><type>session</type><listen>unix:path=%s</listen><auth>EXTERNAL</auth><servicedir>%s</servicedir><policy context="default"><allow user="*"/><allow own="*"/><allow send_destination="*"/><allow receive_sender="*"/></policy></busconfig>`, path, services)
	configPath := filepath.Join(directory, "bus.conf")
	testutil.FailErr(t, "private activation configuration", os.WriteFile(configPath, []byte(config), 0o600))
	cmd := exec.Command("dbus-daemon", "--nofork", "--config-file="+configPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	testutil.FailErr(t, "start activation bus", cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return path, marker
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("activation bus did not start: %s", stderr.String())
	return "", ""
}

func TestPowerActivationFixtureProcess(t *testing.T) {
	if os.Getenv("PW_POWER_ACTIVATION_FIXTURE") != "1" {
		return
	}
	bus, err := dbus.Connect(os.Getenv("DBUS_STARTER_ADDRESS"))
	testutil.FailErr(t, "connect activated service", err)
	defer func() { _ = bus.Close() }()
	testutil.FailErr(t, "export activated provider", bus.Export(&fixturePowerManagement{}, powerManagementPath, powerManagementInhibit))
	ownName(t, bus, powerManagementName)
	testutil.FailErr(t, "record activation", os.WriteFile(os.Getenv("PW_POWER_ACTIVATION_MARKER"), nil, 0o600))
	<-bus.Context().Done()
}

func TestActivatablePowerProviderIsDiscoveredWithoutStartingAndAcquiredOnDemand(t *testing.T) {
	path, marker := activationBus(t)
	bus, err := linuxbus.Connect(t.Context(), path)
	testutil.FailErr(t, "connect activation client", err)
	defer func() { _ = bus.Close() }()
	state, err := linuxbus.DiscoverName(t.Context(), bus, powerManagementName)
	testutil.FailErr(t, "discover installed provider", err)
	if state.Owner != "" || !state.Activatable {
		t.Fatalf("idle provider state = %+v", state)
	}
	subject := powerManagementProvider{connect: connectTo(path)}
	testutil.FailErr(t, "check installed provider", subject.Check(t.Context()))
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discovery started a service: %v", err)
	}
	held, err := acquireFrom([]provider{subject})
	testutil.FailErr(t, "acquire lazily activated provider", err)
	defer func() { _ = held.Release() }()
	state, err = linuxbus.DiscoverName(t.Context(), bus, powerManagementName)
	testutil.FailErr(t, "discover active provider", err)
	if state.Owner == "" || held.Provider() != "power-management" {
		t.Fatalf("active provider state=%+v lease=%s", state, held.Provider())
	}
	testutil.FailErr(t, "release activated inhibition", held.Release())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := linuxbus.DiscoverName(ctx, bus, powerManagementName); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled discovery = %v", err)
	}
	testutil.FailErr(t, "disconnect discovery client", bus.Close())
	if _, err := linuxbus.DiscoverName(t.Context(), bus, powerManagementName); !errors.Is(err, dbus.ErrClosed) {
		t.Fatalf("disconnected discovery = %v", err)
	}
}

type deniedPowerProvider struct{}

func (deniedPowerProvider) Inhibit(string, string) (uint32, *dbus.Error) {
	return 0, dbus.NewError("org.freedesktop.DBus.Error.AccessDenied", []any{"fixture denial"})
}

func TestPowerInvocationRetainsPermissionDenialAfterSuccessfulDiscovery(t *testing.T) {
	path := fixtureBus(t)
	server, err := linuxbus.Connect(t.Context(), path)
	testutil.FailErr(t, "connect denied provider", err)
	defer func() { _ = server.Close() }()
	testutil.FailErr(t, "export denied provider", server.Export(deniedPowerProvider{}, powerManagementPath, powerManagementInhibit))
	ownName(t, server, powerManagementName)
	subject := powerManagementProvider{connect: connectTo(path)}
	testutil.FailErr(t, "discover denied provider", subject.Check(t.Context()))
	held, err := acquireFrom([]provider{subject})
	var unavailable *UnavailableError
	var remote dbus.Error
	if held != nil || !errors.As(err, &unavailable) || len(unavailable.Reasons) != 1 || unavailable.Reasons[0].Code != ReasonPermissionDenied || !errors.As(err, &remote) {
		t.Fatalf("denied native acquisition lease=%v error=%+v remote=%+v", held, err, remote)
	}
}

func TestPowerReasonsFollowStructuredNativeErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code ProviderReasonCode
	}{
		{dbus.Error{Name: "org.freedesktop.DBus.Error.AuthFailed"}, ReasonPermissionDenied},
		{dbus.Error{Name: "org.freedesktop.DBus.Error.InteractiveAuthorizationRequired"}, ReasonPermissionDenied},
		{dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownMethod"}, ReasonUnsupportedOperation},
		{dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownInterface"}, ReasonUnsupportedOperation},
		{dbus.Error{Name: "org.freedesktop.DBus.Error.NotSupported"}, ReasonUnsupportedOperation},
		{dbus.Error{Name: "org.freedesktop.DBus.Error.Disconnected"}, ReasonDisconnected},
		{dbus.Error{Name: "org.freedesktop.DBus.Error.NameHasNoOwner"}, ReasonDisconnected},
		{dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown"}, ReasonDisconnected},
		{dbus.ErrClosed, ReasonDisconnected},
		{context.Canceled, ReasonCanceled},
		{errors.New("AccessDenied in diagnostic text"), ReasonQueryFailed},
	} {
		got := reasonFor(powerManagementProvider{}, fmt.Errorf("invocation: %w", tc.err))
		if got.Code != tc.code {
			t.Fatalf("native cause %T(%v) = %s, want %s", tc.err, tc.err, got.Code, tc.code)
		}
	}
}
