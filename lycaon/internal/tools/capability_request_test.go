package tools_test

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestParseCapabilityRequestAbsent(t *testing.T) {
	req, reject := tools.ParseCapabilityRequest(map[string]any{"command": "echo hi"})
	if reject != nil || req != nil {
		t.Fatalf("absent capability_request = (%v, %v), want nil,nil", req, reject)
	}
}

func TestCapabilityContractIsCatalogCompiled(t *testing.T) {
	command, ok := toolcontract.Lookup("command")
	if !ok {
		t.Fatal("command contract missing")
	}
	for _, capability := range []toolcontract.Capability{
		toolcontract.CapabilityHostResource, toolcontract.CapabilitySocket,
		toolcontract.CapabilityDirectIP, toolcontract.CapabilityLocalListen,
		toolcontract.CapabilityLoopbackConnect, toolcontract.CapabilityTerminalCapture,
		toolcontract.CapabilityWriteRoot,
	} {
		if !command.Supports(capability) {
			t.Fatalf("command lacks capability %d", capability)
		}
	}
	if reject := tools.ValidateCapabilityContract(command, map[string]any{
		"capability_request": map[string]any{"loopback_connect": map[string]any{}},
	}); reject != nil {
		t.Fatalf("command loopback_connect reject = %+v", reject)
	}
	verify, ok := toolcontract.Lookup("verify")
	if !ok {
		t.Fatal("verify contract missing")
	}
	if reject := tools.ValidateCapabilityContract(verify, map[string]any{
		"capability_request": map[string]any{"local_listen": map[string]any{}},
	}); reject != nil {
		t.Fatalf("verify local-listen reject = %+v", reject)
	}
	if reject := tools.ValidateCapabilityContract(verify, map[string]any{
		"capability_request": map[string]any{"write_root": filepath.Join(t.TempDir(), "cache")},
	}); reject != nil {
		t.Fatalf("verify write_root reject = %+v", reject)
	}
}

func TestCompiledContractsMirrorCatalogCapabilities(t *testing.T) {
	cfg, err := nativemanifest.Load()
	testutil.FailErr(t, "nativemanifest.Load", err)
	for tool, names := range cfg.ExecutionCapabilities {
		contract, ok := toolcontract.Lookup(tool)
		if !ok {
			t.Fatalf("catalog tool %q has no compiled contract", tool)
		}
		for _, name := range names {
			capability, ok := toolcontract.NamedCapability(name)
			if !ok {
				t.Fatalf("catalog capability %q on %q has no NamedCapability", name, tool)
			}
			if !contract.Supports(capability) {
				t.Fatalf("compiled %q dropped catalog capability %q", tool, name)
			}
		}
	}
}

func TestParseCapabilityRequestEmptyObjectRejected(t *testing.T) {
	_, reject := tools.ParseCapabilityRequest(map[string]any{
		"capability_request": map[string]any{},
	})
	if reject == nil || reject.Code != "SANDBOX_CAPABILITY_REQUEST_INVALID" {
		t.Fatalf("empty object reject = %+v", reject)
	}
}

func TestParseCapabilityRequestHostResources(t *testing.T) {
	req, reject := tools.ParseCapabilityRequest(map[string]any{
		"capability_request": map[string]any{
			"host_resources": []any{"docker", "aws-cli", "docker"},
		},
	})
	if reject != nil {
		t.Fatalf("unexpected reject: %+v", reject)
	}
	if len(req.HostResources) != 2 || req.HostResources[0] != "aws-cli" || req.HostResources[1] != "docker" {
		t.Fatalf("host resources = %#v", req.HostResources)
	}
}

func TestParseCapabilityRequestDedupesAndSorts(t *testing.T) {
	req, reject := tools.ParseCapabilityRequest(map[string]any{
		"capability_request": map[string]any{
			"socket_paths": []any{"/tmp/b.sock", "/tmp/a.sock", "/tmp/a.sock"},
		},
	})
	if reject != nil {
		t.Fatalf("unexpected reject: %+v", reject)
	}
	if len(req.SocketPaths) != 2 || req.SocketPaths[0] != "/tmp/a.sock" || req.SocketPaths[1] != "/tmp/b.sock" {
		t.Fatalf("paths = %#v", req.SocketPaths)
	}
	if req.DirectIP != nil {
		t.Fatalf("DirectIP = %#v, want nil", req.DirectIP)
	}
}

func TestParseCapabilityRequestOverCapRejected(t *testing.T) {
	paths := make([]any, 0, confine.MaxSocketGrants+1)
	for i := 0; i < confine.MaxSocketGrants+1; i++ {
		paths = append(paths, fmt.Sprintf("/tmp/overcap-%d.sock", i))
	}
	_, reject := tools.ParseCapabilityRequest(map[string]any{
		"capability_request": map[string]any{"socket_paths": paths},
	})
	if reject == nil || reject.Code != "SANDBOX_SOCKET_PATH_LIMIT" {
		t.Fatalf("over cap reject = %+v", reject)
	}
}

func TestParseCapabilityRequestDirectIPTrue(t *testing.T) {
	req, reject := tools.ParseCapabilityRequest(map[string]any{
		"capability_request": map[string]any{"direct_ip": true},
	})
	if reject != nil {
		t.Fatalf("unexpected reject: %+v", reject)
	}
	if req == nil || req.DirectIP == nil {
		t.Fatal("direct_ip: true must yield DirectIP")
	}
	if len(req.DirectIP.DeclaredDestinations) != 0 {
		t.Fatalf("DeclaredDestinations = %#v", req.DirectIP.DeclaredDestinations)
	}
}

func TestParseCapabilityRequestDirectIPFalseRejected(t *testing.T) {
	_, reject := tools.ParseCapabilityRequest(map[string]any{
		"capability_request": map[string]any{"direct_ip": false},
	})
	if reject == nil || reject.Code != "SANDBOX_DIRECT_IP_REQUEST_INVALID" {
		t.Fatalf("false reject = %+v", reject)
	}
}

func TestParseCapabilityRequestDirectIPDestinationList(t *testing.T) {
	req, reject := tools.ParseCapabilityRequest(map[string]any{
		"capability_request": map[string]any{
			"direct_ip": []any{"udp://time.nist.gov:123", "udp://time.google.com:123"},
		},
	})
	if reject != nil {
		t.Fatalf("unexpected reject: %+v", reject)
	}
	if req == nil || req.DirectIP == nil || len(req.DirectIP.DeclaredDestinations) != 2 {
		t.Fatalf("list form = %#v", req)
	}
}

func TestParseCapabilityRequestDirectIPEmptyObject(t *testing.T) {
	req, reject := tools.ParseCapabilityRequest(map[string]any{
		"capability_request": map[string]any{
			"direct_ip": map[string]any{},
		},
	})
	if reject != nil {
		t.Fatalf("unexpected reject: %+v", reject)
	}
	if req == nil || req.DirectIP == nil {
		t.Fatal("direct_ip: {} must yield DirectIP non-nil")
	}
	if len(req.SocketPaths) != 0 {
		t.Fatalf("SocketPaths = %#v, want empty", req.SocketPaths)
	}
	if len(req.DirectIP.DeclaredDestinations) != 0 {
		t.Fatalf("DeclaredDestinations = %#v", req.DirectIP.DeclaredDestinations)
	}
}

func TestParseCapabilityRequestDirectIPDeclaredDedupesAndSorts(t *testing.T) {
	req, reject := tools.ParseCapabilityRequest(map[string]any{
		"capability_request": map[string]any{
			"direct_ip": map[string]any{
				"declared_destinations": []any{"db.example:5432", "api.example:443", "db.example:5432", "  api.example:443  "},
			},
		},
	})
	if reject != nil {
		t.Fatalf("unexpected reject: %+v", reject)
	}
	want := []string{"api.example:443", "db.example:5432"}
	if len(req.DirectIP.DeclaredDestinations) != 2 ||
		req.DirectIP.DeclaredDestinations[0] != want[0] ||
		req.DirectIP.DeclaredDestinations[1] != want[1] {
		t.Fatalf("DeclaredDestinations = %#v, want %#v", req.DirectIP.DeclaredDestinations, want)
	}
}

func TestParseCapabilityRequestDirectIPOverCapRejected(t *testing.T) {
	dests := make([]any, 0, tools.MaxDirectIPDeclaredDestinations+1)
	for i := 0; i < tools.MaxDirectIPDeclaredDestinations+1; i++ {
		dests = append(dests, fmt.Sprintf("host-%d.example:1", i))
	}
	_, reject := tools.ParseCapabilityRequest(map[string]any{
		"capability_request": map[string]any{
			"direct_ip": map[string]any{"declared_destinations": dests},
		},
	})
	if reject == nil || reject.Code != "SANDBOX_DIRECT_IP_REQUEST_INVALID" {
		t.Fatalf("over cap reject = %+v", reject)
	}
}

func TestParseCapabilityRequestDirectIPByteLimitRejected(t *testing.T) {
	tooLong := strings.Repeat("a", tools.MaxDirectIPDeclaredBytes+1)
	_, reject := tools.ParseCapabilityRequest(map[string]any{
		"capability_request": map[string]any{
			"direct_ip": map[string]any{
				"declared_destinations": []any{tooLong},
			},
		},
	})
	if reject == nil || reject.Code != "SANDBOX_DIRECT_IP_REQUEST_INVALID" {
		t.Fatalf("byte limit reject = %+v", reject)
	}
}

func TestParseCapabilityRequestDirectIPControlRejected(t *testing.T) {
	for _, bad := range []string{"host\x00:1", "host\n:1", "host\t:1", "host\x7f:1"} {
		_, reject := tools.ParseCapabilityRequest(map[string]any{
			"capability_request": map[string]any{
				"direct_ip": map[string]any{
					"declared_destinations": []any{bad},
				},
			},
		})
		if reject == nil || reject.Code != "SANDBOX_DIRECT_IP_REQUEST_INVALID" {
			t.Fatalf("control reject for %q = %+v", bad, reject)
		}
	}
}

func TestParseCapabilityRequestSocketAndDirectTogether(t *testing.T) {
	req, reject := tools.ParseCapabilityRequest(map[string]any{
		"capability_request": map[string]any{
			"socket_paths": []any{"/tmp/a.sock"},
			"direct_ip":    map[string]any{},
		},
	})
	if reject != nil {
		t.Fatalf("unexpected reject: %+v", reject)
	}
	if len(req.SocketPaths) != 1 || req.DirectIP == nil {
		t.Fatalf("combined = %#v", req)
	}
}

func TestParseCapabilityRequestUnsupportedFieldRejected(t *testing.T) {
	_, reject := tools.ParseCapabilityRequest(map[string]any{
		"capability_request": map[string]any{"network": true},
	})
	if reject == nil || reject.Code != "SANDBOX_CAPABILITY_REQUEST_INVALID" {
		t.Fatalf("unsupported field reject = %+v", reject)
	}
	_, reject = tools.ParseCapabilityRequest(map[string]any{
		"capability_request": map[string]any{"socks_proxy": true},
	})
	if reject == nil || reject.Code != "SANDBOX_CAPABILITY_REQUEST_INVALID" {
		t.Fatalf("socks_proxy under capability_request must be unsupported: %+v", reject)
	}
}

func TestParseCapabilityRequestAcceptsCatalogFields(t *testing.T) {
	payloads := map[string]any{
		"host_resources":   []any{"docker"},
		"process_control":  true,
		"host_execution":   true,
		"socket_paths":     []any{"/tmp/a.sock"},
		"direct_ip":        map[string]any{},
		"local_listen":     map[string]any{},
		"loopback_connect": map[string]any{},
		"write_root":       filepath.Join(t.TempDir(), "cache"),
		"read_path":        filepath.Join(t.TempDir(), "key.pem"),
	}
	for _, field := range toolcontract.CapabilityRequestFields() {
		raw, ok := payloads[field]
		if !ok {
			t.Fatalf("no parse payload for catalog field %q", field)
		}
		_, reject := tools.ParseCapabilityRequest(map[string]any{
			"capability_request": map[string]any{field: raw},
		})
		if reject != nil {
			t.Fatalf("%s reject = %+v", field, reject)
		}
	}
}

func TestParseCapabilityRequestWriteRootIsClosedAndCanonical(t *testing.T) {
	base := t.TempDir()
	req, reject := tools.ParseCapabilityRequest(map[string]any{"capability_request": map[string]any{
		"write_root": filepath.Join(base, "cache", "..", "cache"),
	}})
	if reject != nil {
		t.Fatalf("write_root reject = %+v", reject)
	}
	if req == nil || req.WriteRoot != filepath.Join(base, "cache") {
		t.Fatalf("write roots = %+v", req)
	}
	_, reject = tools.ParseCapabilityRequest(map[string]any{"capability_request": map[string]any{
		"write_root": "relative/cache",
	}})
	if reject == nil || reject.Code != "SANDBOX_CAPABILITY_REQUEST_INVALID" {
		t.Fatalf("relative write root reject = %+v", reject)
	}
}

func TestParseCapabilityRequestWriteRootDeclaresProtectedLocations(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	for _, root := range []string{home, filepath.Join(home, ".ssh"), filepath.Join(home, ".aws")} {
		req, reject := tools.ParseCapabilityRequest(map[string]any{"capability_request": map[string]any{
			"write_root": root,
		}})
		if reject != nil {
			t.Fatalf("declaring %q rejected: %+v; protected locations ask, they are not silently denied", root, reject)
		}
		if req == nil || req.WriteRoot != filepath.Clean(root) {
			t.Fatalf("declared %q, parsed %+v", root, req)
		}
	}
	_, reject := tools.ParseCapabilityRequest(map[string]any{"capability_request": map[string]any{
		"write_root": "/",
	}})
	if reject == nil || reject.Code != "SANDBOX_CAPABILITY_REQUEST_INVALID" {
		t.Fatalf("filesystem root reject = %+v", reject)
	}
}

// Key paths are declarable for reads — the broker asks; only the control
// plane and the filesystem root are refused before the ask.
func TestParseCapabilityRequestReadPathDeclares(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	req, reject := tools.ParseCapabilityRequest(map[string]any{"capability_request": map[string]any{
		"read_path": filepath.Join(home, ".ssh", "id_ed25519"),
	}})
	if reject != nil {
		t.Fatalf("declaring a key read rejected: %+v", reject)
	}
	if req == nil || req.ReadPath != filepath.Join(home, ".ssh", "id_ed25519") {
		t.Fatalf("parsed %+v", req)
	}
	_, reject = tools.ParseCapabilityRequest(map[string]any{"capability_request": map[string]any{
		"read_path": "relative/key",
	}})
	if reject == nil || reject.Code != "SANDBOX_CAPABILITY_REQUEST_INVALID" {
		t.Fatalf("relative read path reject = %+v", reject)
	}
}

func TestCapabilityPathAuthorityReadRefusesControlPlane(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	reject := tools.ValidateCapabilityPathAuthority(map[string]any{"capability_request": map[string]any{
		"read_path": filepath.Join(cfg, "credential-vault.age"),
	}}, "")
	if reject == nil || reject.Code != "SANDBOX_CONTROL_PLANE_DENIED" {
		t.Fatalf("control-plane read path reject = %+v", reject)
	}
}

func TestCapabilityPathAuthorityWriteRefusesControlPlane(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	reject := tools.ValidateCapabilityPathAuthority(map[string]any{"capability_request": map[string]any{
		"write_root": filepath.Join(cfg, "credential-vault.age"),
	}}, "")
	if reject == nil || reject.Code != "SANDBOX_CONTROL_PLANE_DENIED" {
		t.Fatalf("control-plane write root reject = %+v", reject)
	}
}

func TestCapabilityRequestAllowsPolicyWriteApproval(t *testing.T) {
	project := t.TempDir()
	agents := filepath.Join(project, "AGENTS.md")
	request, reject := tools.ParseCapabilityRequest(map[string]any{"capability_request": map[string]any{
		"read_path": agents,
	}})
	if reject != nil || request == nil || request.ReadPath != agents {
		t.Fatalf("governance read request = %+v reject=%+v", request, reject)
	}
	request, reject = tools.ParseCapabilityRequest(map[string]any{"capability_request": map[string]any{
		"write_root": agents,
	}})
	if reject != nil || request == nil || request.WriteRoot != agents {
		t.Fatalf("policy write request = %+v reject=%+v", request, reject)
	}
}

func TestResolveCapabilitySocketsLive(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "sock") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "MkdirTemp", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "svc.sock")
	ln, err := net.Listen("unix", sockPath)
	testutil.FailErr(t, "net.Listen", err)
	t.Cleanup(func() { _ = ln.Close() })

	grants, reject := tools.ResolveCapabilitySockets(&tools.CapabilityRequest{
		SocketPaths: []string{sockPath},
	})
	if reject != nil {
		t.Fatalf("unexpected reject: %+v", reject)
	}
	if len(grants) != 1 || grants[0].ApprovedPath != sockPath {
		t.Fatalf("grants = %#v", grants)
	}
}

func TestResolveCapabilitySocketsNotSocketRejected(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "sock") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "MkdirTemp", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	filePath := filepath.Join(dir, "not-a-socket")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		testutil.FailErr(t, "os.WriteFile", err)
	}
	_, reject := tools.ResolveCapabilitySockets(&tools.CapabilityRequest{
		SocketPaths: []string{filePath},
	})
	if reject == nil || reject.Code != "SANDBOX_SOCKET_PATH_NOT_SOCKET" {
		t.Fatalf("reject = %+v", reject)
	}
}

func TestResolveCapabilitySocketsDirectOnlyNil(t *testing.T) {
	grants, reject := tools.ResolveCapabilitySockets(&tools.CapabilityRequest{
		DirectIP: &tools.DirectIPRequest{},
	})
	if reject != nil || grants != nil {
		t.Fatalf("direct-only resolve = (%v, %v)", grants, reject)
	}
}

func TestCapabilityArgumentFailuresKeepTheirObservationKind(t *testing.T) {
	_, reject := tools.ParseCapabilityRequest(map[string]any{"capability_request": map[string]any{
		"loopback_connect": map[string]any{"ports": []any{float64(70000)}},
	}})
	if reject == nil || !reject.ArgumentValidation {
		t.Fatal("[OAR-PROF-3] failed capability argument check lost its observation kind")
	}
	completed := tools.CompleteFailureMetadata(reject, "command", "fixture")
	if !completed.ArgumentValidation || completed.Code != reject.Code {
		t.Fatal("failure metadata changed the observed argument-check result")
	}
}

func TestCapabilityPathAuthorityLeavesOnlyInvokingScratch(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	scratch := filepath.Join(cfg, "scratch", "own")
	for _, field := range []string{"read_path", "write_root"} {
		for _, path := range []string{filepath.Join(scratch, "file"), filepath.Join(cfg, "scratch", "other", "file"), filepath.Join(cfg, "store.db")} {
			reject := tools.ValidateCapabilityPathAuthority(map[string]any{"capability_request": map[string]any{field: path}}, scratch)
			own := path == filepath.Join(scratch, "file")
			if (reject == nil) != own {
				t.Fatalf("%s %s reject=%+v", field, path, reject)
			}
		}
	}
}
