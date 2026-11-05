//go:build linux

package confine_test

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lycaon/lycaon/internal/linuxbus"
	"github.com/lycaon/lycaon/internal/testutil"
)

const credentialProbeEnv = "PW_CREDENTIAL_SERVICE_PROBE"
const credentialProbeFlag = "-test.run=^TestLinuxCredentialServiceProbeProcess$"
const credentialRoot dbus.ObjectPath = "/org/freedesktop/secrets"
const credentialItem dbus.ObjectPath = "/org/freedesktop/secrets/collection/test/item"
const credentialSession dbus.ObjectPath = "/org/freedesktop/secrets/session/test"

type sandboxCredentialSecret struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

type sandboxCredentialService struct{ mutations atomic.Uint32 }

func (*sandboxCredentialService) OpenSession(string, dbus.Variant) (dbus.Variant, dbus.ObjectPath, *dbus.Error) {
	return dbus.MakeVariant(""), credentialSession, nil
}

func (*sandboxCredentialService) GetSecret(session dbus.ObjectPath) (sandboxCredentialSecret, *dbus.Error) {
	return sandboxCredentialSecret{Session: session, Value: []byte("fixture-credential"), ContentType: "text/plain"}, nil
}

func (s *sandboxCredentialService) SetSecret(sandboxCredentialSecret) *dbus.Error {
	s.mutations.Add(1)
	return nil
}

func (*sandboxCredentialService) Close() *dbus.Error { return nil }

func TestLinuxCredentialServiceProbeProcess(t *testing.T) {
	if os.Getenv(credentialProbeEnv) == "" {
		return
	}
	os.Exit(credentialServiceProbe())
}

func credentialServiceProbe() int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bus, err := linuxbus.ConnectSession(ctx)
	if err != nil {
		fmt.Println("connect:", err)
		return 1
	}
	defer bus.Close()
	var output dbus.Variant
	var session dbus.ObjectPath
	if err := bus.Object("org.freedesktop.secrets", credentialRoot).CallWithContext(ctx, "org.freedesktop.Secret.Service.OpenSession", 0, "plain", dbus.MakeVariant("")).Store(&output, &session); err != nil {
		fmt.Println("session:", err)
		return 2
	}
	item := bus.Object("org.freedesktop.secrets", credentialItem)
	var secret sandboxCredentialSecret
	if err := item.CallWithContext(ctx, "org.freedesktop.Secret.Item.GetSecret", 0, session).Store(&secret); err != nil || string(secret.Value) != "fixture-credential" {
		fmt.Println("read:", err)
		return 3
	}
	if err := item.CallWithContext(ctx, "org.freedesktop.Secret.Item.SetSecret", 0, secret).Err; err == nil {
		fmt.Println("mutation admitted")
		return 4
	}
	if err := bus.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.RequestName", 0, "dev.paintedwolf.Unconfined", uint32(0)).Err; err == nil {
		fmt.Println("bus administration admitted")
		return 5
	}
	if err := bus.Object("org.freedesktop.secrets", session).CallWithContext(ctx, "org.freedesktop.Secret.Session.Close", 0).Err; err != nil {
		fmt.Println("close:", err)
		return 6
	}
	fmt.Println("credential read confined; mutations and bus administration denied")
	return 0
}

func TestLinuxCredentialReadMediationWorksThroughTheAppliedSandbox(t *testing.T) {
	self := requireLinuxSandbox(t)
	directory, err := os.MkdirTemp("/tmp", "pw-credential-test-") //nolint:usetesting // Unix socket paths are bounded.
	testutil.FailErr(t, "create private bus directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	path := filepath.Join(directory, "bus")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, requireTool(t, "dbus-daemon"), "--session", "--nofork", "--nopidfile", "--address=unix:path="+path, "--print-address=1")
	stdout, err := command.StdoutPipe()
	testutil.FailErr(t, "open fixture bus readiness pipe", err)
	testutil.FailErr(t, "start fixture bus", command.Start())
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	_, err = bufio.NewReader(stdout).ReadString('\n')
	testutil.FailErr(t, "wait for fixture bus", err)
	bus, err := linuxbus.Connect(ctx, path)
	testutil.FailErr(t, "connect fixture service", err)
	t.Cleanup(func() { _ = bus.Close() })
	service := &sandboxCredentialService{}
	for path, iface := range map[dbus.ObjectPath]string{
		credentialRoot: "org.freedesktop.Secret.Service", credentialItem: "org.freedesktop.Secret.Item", credentialSession: "org.freedesktop.Secret.Session",
	} {
		testutil.FailErr(t, "export fixture service", bus.Export(service, path, iface))
	}
	_, err = bus.RequestName("org.freedesktop.secrets", dbus.NameFlagDoNotQueue)
	testutil.FailErr(t, "own fixture credential service", err)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+path)
	environment := append(os.Environ(), credentialProbeEnv+"=1")
	base := denyConfinement(t.TempDir())
	status, _ := linuxConfinedRun(t, self, base, environment, self, credentialProbeFlag)
	if status.ExitStatus() == 0 {
		t.Fatal("ordinary confinement reached a host credential service")
	}
	base.CredentialStore = true
	status, output := linuxConfinedRun(t, self, base, environment, self, credentialProbeFlag)
	if status.ExitStatus() != 0 || !strings.Contains(output, "credential read confined; mutations and bus administration denied") || service.mutations.Load() != 0 {
		t.Fatalf("credential mediation failed: exit=%d mutations=%d output=%s", status.ExitStatus(), service.mutations.Load(), output)
	}
}
