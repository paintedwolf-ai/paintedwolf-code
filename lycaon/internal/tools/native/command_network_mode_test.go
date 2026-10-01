package native

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/packageexec"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Command results state the applied network mode.
func TestCommandResultReportsTheAppliedNetworkMode(t *testing.T) {
	for _, tc := range []struct {
		name     string
		boundary confine.Boundary
		want     string
	}{
		{"proxy_only", confine.Boundary{Applied: true, Network: confine.NetworkProxyOnly}, "proxy_only"},
		{"direct_ip", confine.Boundary{Applied: true, Network: confine.NetworkDirectIP}, "direct_ip"},
		{"deny", confine.Boundary{Applied: true, Network: confine.NetworkDeny}, "deny"},
		{"unconfined", confine.Boundary{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := boundaryNetworkMode(tc.boundary); got != tc.want {
				t.Fatalf("network mode for %s: got %q want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestCommandResultReportsSocksProxyEnv(t *testing.T) {
	with := commandResultFromOutcome(commandRunOutcome{
		Boundary: confine.Boundary{Applied: true, Network: confine.NetworkProxyOnly, SocksProxyEnv: true},
	}, confine.LocalNetworkGrant{}, nil)
	payload, err := json.Marshal(with)
	testutil.FailErr(t, "marshal with SOCKS proxy", err)
	var decoded map[string]any
	testutil.FailErr(t, "decode with SOCKS proxy", json.Unmarshal(payload, &decoded))
	if decoded["socks_proxy"] != true {
		t.Fatalf("socks_proxy = %v, want true", decoded["socks_proxy"])
	}

	without := commandResultFromOutcome(commandRunOutcome{
		Boundary: confine.Boundary{Applied: true, Network: confine.NetworkProxyOnly},
	}, confine.LocalNetworkGrant{}, nil)
	payload, err = json.Marshal(without)
	testutil.FailErr(t, "marshal without SOCKS proxy", err)
	decoded = map[string]any{}
	testutil.FailErr(t, "decode without SOCKS proxy", json.Unmarshal(payload, &decoded))
	if _, present := decoded["socks_proxy"]; present {
		t.Fatal("socks_proxy must be omitted when not applied")
	}
}

func TestCommandResultOmitsNetworkModeWhenUnconfined(t *testing.T) {
	confined := commandResultFromOutcome(commandRunOutcome{
		Boundary: confine.Boundary{Applied: true, Network: confine.NetworkProxyOnly},
	}, confine.LocalNetworkGrant{}, nil)
	payload, err := json.Marshal(confined)
	testutil.FailErr(t, "marshal confined result", err)
	var decoded map[string]any
	testutil.FailErr(t, "decode confined result", json.Unmarshal(payload, &decoded))
	if decoded["network_mode"] != "proxy_only" {
		t.Fatalf("confined result must name its egress mode: %s", payload)
	}

	unconfined := commandResultFromOutcome(commandRunOutcome{}, confine.LocalNetworkGrant{}, nil)
	payload, err = json.Marshal(unconfined)
	testutil.FailErr(t, "marshal unconfined result", err)
	decoded = map[string]any{}
	testutil.FailErr(t, "decode unconfined result", json.Unmarshal(payload, &decoded))
	if _, present := decoded["network_mode"]; present {
		t.Fatalf("an unconfined run has no boundary to name: %s", payload)
	}
}

func TestCommandResultStatesRemotePackageBoundaryOnSuccess(t *testing.T) {
	result := commandResultFromOutcome(commandRunOutcome{
		Boundary: confine.Boundary{Applied: true, Network: confine.NetworkProxyOnly},
	}, confine.LocalNetworkGrant{}, &packageexec.Execution{
		AllowedHosts: []string{"proxy.golang.org", "sum.golang.org"},
	})
	if result.RemotePackageExecution == nil {
		t.Fatal("remote package boundary missing")
	}
	if result.RemotePackageExecution.NetworkScope != confine.RemotePackageNetworkScopeRegistryOnly ||
		!result.RemotePackageExecution.AmbientCredentialsRemoved ||
		!result.RemotePackageExecution.ProtectedReadsDenied {
		t.Fatalf("remote package boundary = %+v", result.RemotePackageExecution)
	}
}
