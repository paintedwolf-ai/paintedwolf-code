//go:build linux

package linuxbus

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lycaon/lycaon/internal/testutil"
)

const credentialFixtureRoot dbus.ObjectPath = "/org/freedesktop/secrets"
const credentialFixtureItem dbus.ObjectPath = "/org/freedesktop/secrets/collection/test/item"
const credentialFixturePrompt dbus.ObjectPath = "/org/freedesktop/secrets/prompt/test"

type credentialFixture struct {
	bus       *dbus.Conn
	sessions  atomic.Uint32
	mutations atomic.Uint32
	closed    atomic.Uint32
}

func TestCredentialPoolBoundsAdmissionAndJoinsEveryAcceptedConnection(t *testing.T) {
	policy, err := LoadCredentialReadPolicy()
	testutil.FailErr(t, "load credential policy", err)
	pool, err := NewCredentialReadPool(policy)
	testutil.FailErr(t, "freeze credential policy", err)
	t.Cleanup(func() { _ = pool.Close() })
	// Mutating the caller's catalog after admission cannot widen the helper's
	// policy. Its private copy owns the complete method/interface graph.
	delete(policy.Services[0].Interfaces, "org.freedesktop.Secret.Item")
	if _, ok := pool.policy.Services[0].Interfaces["org.freedesktop.Secret.Item"]; !ok {
		t.Fatal("admitted policy retained caller-owned mutable state")
	}
	path := fixtureBus(t)
	for range maxCredentialConnections {
		credentialFixtureClient(t, pool, path)
	}
	client, incoming := net.Pipe()
	upstream, service := net.Pipe()
	defer client.Close()
	defer service.Close()
	if err := pool.Start(incoming, upstream); err == nil {
		t.Fatal("connection beyond bound was admitted")
	}
	if _, err := client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("refused connection was not closed: %v", err)
	}
	testutil.FailErr(t, "join incomplete authentication connections", pool.Close())
	pool.mu.Lock()
	settled := pool.closed && len(pool.active) == 0
	pool.mu.Unlock()
	if !settled {
		t.Fatal("close returned with a live mediated connection")
	}
	client, incoming = net.Pipe()
	upstream, service = net.Pipe()
	defer client.Close()
	defer service.Close()
	if err := pool.Start(incoming, upstream); err == nil {
		t.Fatal("closed admission accepted a late connection")
	}
}

func (f *credentialFixture) OpenSession(string, dbus.Variant) (dbus.Variant, dbus.ObjectPath, *dbus.Error) {
	path := dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/secrets/session/%d", f.sessions.Add(1)))
	if err := f.bus.Export(f, path, "org.freedesktop.Secret.Session"); err != nil {
		return dbus.Variant{}, "/", dbus.MakeFailedError(err)
	}
	return dbus.MakeVariant(""), path, nil
}

func (*credentialFixture) SearchItems(map[string]string) ([]dbus.ObjectPath, []dbus.ObjectPath, *dbus.Error) {
	return []dbus.ObjectPath{credentialFixtureItem}, []dbus.ObjectPath{}, nil
}

func (*credentialFixture) GetSecrets(items []dbus.ObjectPath, session dbus.ObjectPath) (map[dbus.ObjectPath]credentialSecret, *dbus.Error) {
	secrets := make(map[dbus.ObjectPath]credentialSecret)
	for _, item := range items {
		secrets[item] = credentialSecret{Session: session, Value: []byte("fixture-value"), ContentType: "text/plain"}
	}
	return secrets, nil
}

func (*credentialFixture) GetSecret(session dbus.ObjectPath) (credentialSecret, *dbus.Error) {
	return credentialSecret{Session: session, Value: []byte("fixture-value"), ContentType: "text/plain"}, nil
}

func (*credentialFixture) Unlock([]dbus.ObjectPath) ([]dbus.ObjectPath, dbus.ObjectPath, *dbus.Error) {
	return []dbus.ObjectPath{}, credentialFixturePrompt, nil
}

func (f *credentialFixture) Prompt(string) *dbus.Error {
	if err := f.bus.Emit(credentialFixturePrompt, "org.freedesktop.Secret.Prompt.Completed", false, dbus.MakeVariant([]dbus.ObjectPath{credentialFixtureItem})); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func (*credentialFixture) Dismiss() *dbus.Error { return nil }

func (f *credentialFixture) Close() *dbus.Error {
	f.closed.Add(1)
	return nil
}

func (f *credentialFixture) SetSecret(credentialSecret) *dbus.Error {
	f.mutations.Add(1)
	return nil
}

func (f *credentialFixture) Delete() (dbus.ObjectPath, *dbus.Error) {
	f.mutations.Add(1)
	return "/", nil
}

func (f *credentialFixture) CreateCollection(map[string]dbus.Variant, string) (dbus.ObjectPath, dbus.ObjectPath, *dbus.Error) {
	f.mutations.Add(1)
	return credentialFixtureRoot, "/", nil
}

func (f *credentialFixture) Set(string, string, dbus.Variant) *dbus.Error {
	f.mutations.Add(1)
	return nil
}

func credentialFixtureService(t *testing.T, path string) *credentialFixture {
	t.Helper()
	bus, err := Connect(t.Context(), path)
	testutil.FailErr(t, "connect credential fixture", err)
	t.Cleanup(func() { _ = bus.Close() })
	f := &credentialFixture{bus: bus}
	for object, interfaces := range map[dbus.ObjectPath][]string{
		credentialFixtureRoot:   {"org.freedesktop.Secret.Service"},
		credentialFixtureItem:   {"org.freedesktop.Secret.Item", "org.freedesktop.DBus.Properties"},
		credentialFixturePrompt: {"org.freedesktop.Secret.Prompt"},
	} {
		for _, iface := range interfaces {
			testutil.FailErr(t, "export credential fixture", bus.Export(f, object, iface))
		}
	}
	name, err := bus.RequestName("org.freedesktop.secrets", dbus.NameFlagDoNotQueue)
	testutil.FailErr(t, "request credential service name", err)
	if name != dbus.RequestNameReplyPrimaryOwner {
		t.Fatal("fixture did not own credential service")
	}
	return f
}

func credentialFixtureClient(t *testing.T, pool *CredentialReadPool, path string) *dbus.Conn {
	t.Helper()
	upstream, err := (&net.Dialer{}).DialContext(t.Context(), "unix", path)
	testutil.FailErr(t, "dial fixture bus", err)
	client, proxy := net.Pipe()
	testutil.FailErr(t, "start mediated credential connection", pool.Start(proxy, upstream))
	bus, err := dbus.NewConn(client, dbus.WithContext(t.Context()))
	testutil.FailErr(t, "create credential client", err)
	t.Cleanup(func() { _ = bus.Close() })
	testutil.FailErr(t, "authenticate credential client", bus.Auth(nil))
	testutil.FailErr(t, "greet credential client", bus.Hello())
	return bus
}

func TestCredentialReadProtocolPreservesReadsAndConnectionBoundLifecycle(t *testing.T) {
	path := fixtureBus(t)
	fixture := credentialFixtureService(t, path)
	policy, err := LoadCredentialReadPolicy()
	testutil.FailErr(t, "load credential policy", err)
	pool, err := NewCredentialReadPool(policy)
	testutil.FailErr(t, "freeze credential policy", err)
	t.Cleanup(func() { _ = pool.Close() })
	client := credentialFixtureClient(t, pool, path)
	service := client.Object("org.freedesktop.secrets", credentialFixtureRoot)
	var output dbus.Variant
	var session dbus.ObjectPath
	testutil.FailErr(t, "open read session", service.CallWithContext(t.Context(), "org.freedesktop.Secret.Service.OpenSession", 0, "plain", dbus.MakeVariant("")).Store(&output, &session))
	for _, destination := range []string{"org.freedesktop.secrets", fixture.bus.Names()[0]} {
		for _, items := range [][]dbus.ObjectPath{{}, {credentialFixtureItem}} {
			var secrets map[dbus.ObjectPath]credentialSecret
			testutil.FailErr(t, "retrieve credentials", client.Object(destination, credentialFixtureRoot).CallWithContext(t.Context(), "org.freedesktop.Secret.Service.GetSecrets", 0, items, session).Store(&secrets))
			if len(secrets) != len(items) || len(items) > 0 && string(secrets[credentialFixtureItem].Value) != "fixture-value" {
				t.Fatalf("credential result changed: %+v", secrets)
			}
		}
		var secret credentialSecret
		testutil.FailErr(t, "retrieve one credential", client.Object(destination, credentialFixtureItem).CallWithContext(t.Context(), "org.freedesktop.Secret.Item.GetSecret", 0, session).Store(&secret))
		if string(secret.Value) != "fixture-value" {
			t.Fatal("single credential value changed")
		}
	}
	other := credentialFixtureClient(t, pool, path)
	if err := other.Object("org.freedesktop.secrets", credentialFixtureItem).CallWithContext(t.Context(), "org.freedesktop.Secret.Item.GetSecret", 0, session).Err; err == nil {
		t.Fatal("another connection borrowed a read session")
	}
	if err := client.Object("org.freedesktop.secrets", credentialFixturePrompt).CallWithContext(t.Context(), "org.freedesktop.Secret.Prompt.Prompt", 0, "").Err; err == nil {
		t.Fatal("an unissued prompt was invoked")
	}
	testutil.FailErr(t, "subscribe to read prompt", client.AddMatchSignalContext(t.Context(), dbus.WithMatchSender("org.freedesktop.secrets"), dbus.WithMatchInterface("org.freedesktop.Secret.Prompt")))
	signals := make(chan *dbus.Signal, 4)
	client.Signal(signals)
	var unlocked []dbus.ObjectPath
	var prompt dbus.ObjectPath
	testutil.FailErr(t, "unlock existing item", service.CallWithContext(t.Context(), "org.freedesktop.Secret.Service.Unlock", 0, []dbus.ObjectPath{credentialFixtureItem}).Store(&unlocked, &prompt))
	testutil.FailErr(t, "present read prompt", client.Object("org.freedesktop.secrets", prompt).CallWithContext(t.Context(), "org.freedesktop.Secret.Prompt.Prompt", 0, "").Err)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	select {
	case signal := <-signals:
		if signal.Name != "org.freedesktop.Secret.Prompt.Completed" || signal.Path != prompt {
			t.Fatalf("wrong prompt completion: %+v", signal)
		}
	case <-deadline.C:
		t.Fatal("read prompt did not complete")
	}
	testutil.FailErr(t, "close read session", client.Object("org.freedesktop.secrets", session).CallWithContext(t.Context(), "org.freedesktop.Secret.Session.Close", 0).Err)
	if err := client.Object("org.freedesktop.secrets", credentialFixtureItem).CallWithContext(t.Context(), "org.freedesktop.Secret.Item.GetSecret", 0, session).Err; err == nil || fixture.closed.Load() != 1 {
		t.Fatal("closed read session remained usable")
	}
}

func TestCredentialReadProtocolDeniesMutationsAndUnrelatedBusAuthority(t *testing.T) {
	path := fixtureBus(t)
	fixture := credentialFixtureService(t, path)
	policy, err := LoadCredentialReadPolicy()
	testutil.FailErr(t, "load credential policy", err)
	pool, err := NewCredentialReadPool(policy)
	testutil.FailErr(t, "freeze credential policy", err)
	t.Cleanup(func() { _ = pool.Close() })
	client := credentialFixtureClient(t, pool, path)
	for _, operation := range []struct {
		destination string
		path        dbus.ObjectPath
		method      string
		args        []any
	}{
		{"org.freedesktop.secrets", credentialFixtureRoot, "org.freedesktop.Secret.Service.CreateCollection", []any{map[string]dbus.Variant{}, "default"}},
		{"org.freedesktop.secrets", credentialFixtureRoot, "org.freedesktop.Secret.Service.Lock", []any{[]dbus.ObjectPath{credentialFixtureItem}}},
		{"org.freedesktop.secrets", credentialFixtureRoot, "org.freedesktop.Secret.Service.SetAlias", []any{"default", credentialFixtureItem}},
		{"org.freedesktop.secrets", credentialFixtureItem, "org.freedesktop.Secret.Item.SetSecret", []any{credentialSecret{Session: "/", Value: []byte("changed")}}},
		{"org.freedesktop.secrets", credentialFixtureItem, "org.freedesktop.Secret.Item.Delete", nil},
		{"org.freedesktop.secrets", credentialFixtureItem, "org.freedesktop.DBus.Properties.Set", []any{"org.freedesktop.Secret.Item", "Label", dbus.MakeVariant("changed")}},
		{"org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus.RequestName", []any{"org.test.Confined", uint32(0)}},
		{"org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus.StartServiceByName", []any{"org.test.Unrelated", uint32(0)}},
		{"org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus.Monitoring.BecomeMonitor", []any{[]string{}, uint32(0)}},
		{"org.kde.kwalletd6", "/modules/kwalletd6", "org.kde.KWallet.open", []any{"missing", int64(0), "test"}},
		{"org.test.Unrelated", credentialFixtureItem, "org.freedesktop.Secret.Item.GetSecret", []any{dbus.ObjectPath("/")}},
	} {
		t.Run(operation.method, func(t *testing.T) {
			err := client.Object(operation.destination, operation.path).CallWithContext(t.Context(), operation.method, 0, operation.args...).Err
			if err == nil {
				t.Fatal("read approval admitted broader authority")
			}
		})
	}
	if fixture.mutations.Load() != 0 {
		t.Fatal("a credential mutation reached the backend")
	}
	testutil.FailErr(t, "close credential pool", pool.Close())
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if !pool.closed || len(pool.active) != 0 {
		t.Fatal("pool close returned before physical connection settlement")
	}
}
