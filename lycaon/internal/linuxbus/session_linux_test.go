//go:build linux

package linuxbus

import (
	"reflect"
	"strings"
	"testing"
)

func environment(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestSessionSelectionPreservesNativeSessionFacts(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want Session
	}{
		{"default runtime", nil, Session{Source: SessionSourceDefaultRuntime, Addresses: []SocketAddress{{Path: "/run/user/1234/bus"}}}},
		{"empty address falls back", map[string]string{"DBUS_SESSION_BUS_ADDRESS": ""}, Session{Source: SessionSourceDefaultRuntime, Addresses: []SocketAddress{{Path: "/run/user/1234/bus"}}}},
		{"custom runtime", map[string]string{"XDG_RUNTIME_DIR": "/custom/session"}, Session{Source: SessionSourceRuntimeDirectory, Addresses: []SocketAddress{{Path: "/custom/session/bus"}}}},
		{"runtime alias", map[string]string{"XDG_RUNTIME_DIR": "/custom/link/../session"}, Session{Source: SessionSourceRuntimeDirectory, Addresses: []SocketAddress{{Path: "/custom/link/../session/bus"}}}},
		{"explicit path", map[string]string{"DBUS_SESSION_BUS_ADDRESS": "unix:path=/session/one", "XDG_RUNTIME_DIR": "/other"}, Session{Source: SessionSourceAddress, Addresses: []SocketAddress{{Path: "/session/one"}}}},
		{"escaped path and guid", map[string]string{"DBUS_SESSION_BUS_ADDRESS": "unix:path=/session/a%20b,guid=0123456789abcdef0123456789abcdef"}, Session{Source: SessionSourceAddress, Addresses: []SocketAddress{{Path: "/session/a b"}}}},
		{"abstract socket", map[string]string{"DBUS_SESSION_BUS_ADDRESS": "unix:abstract=session%2Cone"}, Session{Source: SessionSourceAddress, Addresses: []SocketAddress{{Path: "session,one", Abstract: true}}}},
		{"address alternatives", map[string]string{"DBUS_SESSION_BUS_ADDRESS": "unix:path=/one;unix:abstract=/two"}, Session{Source: SessionSourceAddress, Addresses: []SocketAddress{{Path: "/one"}, {Path: "/two", Abstract: true}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SelectSession(environment(tc.env), 1234)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("session = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
	abstract := SocketAddress{Path: "name", Abstract: true}
	if abstract.dialAddress() != "\x00name" || abstract.String() != "abstract:name" || (SocketAddress{Path: "/p"}).String() != "/p" {
		t.Fatal("socket address rendering changed")
	}
}

func TestSessionSelectionRefusesInvalidAndNonlocalTransports(t *testing.T) {
	for _, address := range []string{
		"autolaunch:", "tcp:host=localhost,port=1234", "unix:runtime=yes", "unix:path=relative",
		"unix:path=/one,path=/two", "unix:path=/one,abstract=two", "unix:path=", "unix:guid=0123",
		"unix:path=/one,guid=bad", "unix:path=/a%00b", "unix:path=/a%0db", "unix:path=/a%0ab",
		"unix:path=/a%", "unix:path=/a%zz", "unix:path=/a b", "unix:path=/a,b", "unix:path=/one;",
		strings.Repeat("x", (64<<10)+1), strings.Repeat("unix:path=/one;", 16) + "unix:path=/last",
	} {
		if session, err := SelectSession(environment(map[string]string{"DBUS_SESSION_BUS_ADDRESS": address}), 1234); err == nil || session.Addresses != nil {
			t.Fatalf("invalid address %q supplied %+v, %v", address, session, err)
		}
	}
	for _, runtime := range []string{"relative", "/a\x00b", "/a\nb"} {
		if session, err := SelectSession(environment(map[string]string{"XDG_RUNTIME_DIR": runtime}), 1234); err == nil || session.Addresses != nil {
			t.Fatalf("invalid runtime %q supplied %+v, %v", runtime, session, err)
		}
	}
	if _, err := (Session{}).Connect(t.Context()); err == nil {
		t.Fatal("empty session connected")
	}
}

func TestSystemSelectionHonoursExplicitAddress(t *testing.T) {
	addresses, err := SelectSystem(environment(nil))
	if err != nil || !reflect.DeepEqual(addresses, []SocketAddress{{Path: defaultSystemBusPath}}) {
		t.Fatalf("default system bus = %+v, %v", addresses, err)
	}
	addresses, err = SelectSystem(environment(map[string]string{"DBUS_SYSTEM_BUS_ADDRESS": "unix:path=/custom/system"}))
	if err != nil || !reflect.DeepEqual(addresses, []SocketAddress{{Path: "/custom/system"}}) {
		t.Fatalf("explicit system bus = %+v, %v", addresses, err)
	}
	if _, err := SelectSystem(environment(map[string]string{"DBUS_SYSTEM_BUS_ADDRESS": "tcp:host=x"})); err == nil {
		t.Fatal("non-local system bus was accepted")
	}
}
