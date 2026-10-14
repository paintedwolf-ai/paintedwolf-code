package linuxsandbox

import (
	"errors"
	"reflect"
	"testing"
)

func TestPlanRoundTripSortsAndDedupes(t *testing.T) {
	t.Parallel()
	in := Plan{
		Version: PlanVersion,
		Attest:  Attest{HostNamespaces: map[string]string{NamespaceUser: "user:[1]"}},
		Filesystem: Filesystem{
			WriteRoots: []string{"/b", "/a", "/a", ""},
			Hidden:     []string{"/z/y", "/z"},
			Restored:   []string{"/z/y/ok"},
		},
		Network: Network{Mode: NetworkDeny},
	}
	raw, err := in.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out, err := DecodePlan(raw)
	if err != nil {
		t.Fatalf("DecodePlan: %v", err)
	}
	if got, want := out.Filesystem.WriteRoots, []string{"/a", "/b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("WriteRoots = %v, want %v", got, want)
	}
	if got, want := out.Filesystem.Hidden, []string{"/z", "/z/y"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Hidden = %v, want %v", got, want)
	}
	if out.Attest.HostNamespaces[NamespaceUser] != "user:[1]" || out.Network.Mode != NetworkDeny {
		t.Fatalf("round trip lost fields: %+v", out)
	}
}

func TestDecodePlanRefusesOtherVersions(t *testing.T) {
	t.Parallel()
	_, err := DecodePlan([]byte(`{"version":2}`))
	var unsupported *Unsupported
	if !errors.As(err, &unsupported) || unsupported.Code != UnsupportedPlanVersion {
		t.Fatalf("version 2: %v", err)
	}
	if _, err := DecodePlan([]byte(`{`)); err == nil {
		t.Fatal("malformed plan decoded")
	}
}

func TestValidateNamesWhatTheHelperCannotExpress(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		net  Network
		code string
	}{
		{"unknown mode", Network{Mode: "tunnel"}, UnsupportedNetworkMode},
		{"proxy without endpoint", Network{Mode: NetworkProxyOnly}, UnsupportedNetworkGrant},
		{"direct udp", Network{Mode: NetworkDirectIP, DirectIPPermits: []DirectIPPermit{{Protocol: "udp", Port: 53}}}, UnsupportedNetworkGrant},
		{"browser", Network{Mode: NetworkDeny, Browser: true, LoopbackConnect: true}, UnsupportedBrowser},
		{"relative socket", Network{Mode: NetworkDeny, SocketPaths: []string{"run/x.sock"}}, UnsupportedNetworkGrant},
	}
	for _, tc := range cases {
		err := Plan{Version: PlanVersion, Network: tc.net}.Validate()
		var unsupported *Unsupported
		if !errors.As(err, &unsupported) || unsupported.Code != tc.code {
			t.Errorf("%s: err = %v, want code %s", tc.name, err, tc.code)
		}
		if err != nil && err.Error() == "" {
			t.Errorf("%s: empty message", tc.name)
		}
	}
	if err := (Plan{Version: PlanVersion, Network: Network{Mode: NetworkDeny}}).Validate(); err != nil {
		t.Fatalf("deny plan refused: %v", err)
	}
	for _, n := range []Network{
		{Mode: NetworkProxyOnly, ProxyAddr: "127.0.0.1:1", LoopbackConnect: true, LoopbackConnectPorts: []uint16{5432}},
		{Mode: NetworkDeny, Listen: true, ListenPorts: []uint16{3000}, SocketPaths: []string{"/run/x.sock"}},
		{Mode: NetworkDirectIP, DirectIPPermits: []DirectIPPermit{{Protocol: "tcp", Port: 443}}},
		{Mode: NetworkDirectIP},
	} {
		if err := (Plan{Version: PlanVersion, Network: n}).Validate(); err != nil {
			t.Fatalf("plan %+v refused: %v", n, err)
		}
	}
	if err := (Plan{Version: 0, Network: Network{Mode: NetworkDeny}}).Validate(); err == nil {
		t.Fatal("version 0 validated")
	}
}
