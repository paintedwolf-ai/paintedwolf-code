package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/packageexec"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestPackageBoundaryRejectsAuthorityThatWouldEscapeFirstRun(t *testing.T) {
	request := &CapabilityRequest{
		HostResources:   []string{"docker"},
		SocketPaths:     []string{"/var/run/docker.sock"},
		DirectIP:        &DirectIPRequest{},
		LocalListen:     &LocalListenRequest{},
		LoopbackConnect: &LoopbackConnectRequest{},
		ReadPath:        "/Users/example/.ssh/id_ed25519",
		WriteRoot:       "/Users/example/Library/Caches/tool",
	}
	got := packageBoundaryWidening(request, true)
	for _, capability := range []string{
		"direct_ip", "host_resources", "local_listen", "loopback_connect",
		"socket_paths", "socks_proxy",
	} {
		if !slices.Contains(got, capability) {
			t.Errorf("blocked capabilities = %v, missing %q", got, capability)
		}
	}
	if slices.Contains(got, "write_root") {
		t.Fatalf("ordinary package cache write root was blocked: %v", got)
	}
	if slices.Contains(got, "read_path") {
		t.Fatalf("explicit protected read was blocked before approval: %v", got)
	}
}

func TestPackageReadAccessRequiresSuccessfulExplicitPreflight(t *testing.T) {
	for _, tool := range []string{"command", "verify", "terminal_open", "catalog_tool"} {
		for _, approved := range []bool{false, true} {
			t.Run(tool+"/approved="+fmt.Sprint(approved), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "cacert.pem")
				other := filepath.Join(t.TempDir(), "credentials")
				calls := 0
				executor := &DefaultToolExecutor{readPathPreflight: func(_ context.Context, gotTool string, _ map[string]any, _ ToolContext, gotPath string) (bool, bool, string, error) {
					calls++
					if gotTool != tool || gotPath != path {
						t.Fatalf("reviewed %q %q, want %q %q", gotTool, gotPath, tool, path)
					}
					return approved, !approved, "", nil
				}}
				tc := ToolContext{
					Invocation:       Invocation{Contract: toolcontract.Contract{Capabilities: toolcontract.CapabilityReadPath}},
					PackageExecution: &packageexec.Execution{SensitiveReads: []string{path, other}},
					SessionReadPaths: []string{other},
				}
				args := map[string]any{"capability_request": map[string]any{"read_path": path}}
				testutil.FailErr(t, "route package read to approval", executor.rejectPackageBoundaryWidening(t.Context(), tool, "", args, tc))
				err := executor.preflightReadPath(t.Context(), tool, args, &tc)
				if calls != 1 || (err == nil) != approved {
					t.Fatalf("preflight calls=%d error=%v approved=%v", calls, err, approved)
				}
				inputs := ActionConfineInputsForContext(tc, nil)
				if approved {
					if !slices.Equal(inputs.OverlayReadPaths, []string{path}) {
						t.Fatalf("approved read paths = %v", inputs.OverlayReadPaths)
					}
				} else if len(inputs.OverlayReadPaths) != 0 {
					t.Fatalf("denied approval installed read paths: %v", inputs.OverlayReadPaths)
				}
				if !slices.Equal(inputs.ReadDenyPaths, []string{path, other}) {
					t.Fatalf("read exclusions changed: %v", inputs.ReadDenyPaths)
				}
			})
		}
	}
}

func TestPackageBoundaryDoesNotInheritSessionLocalNetworkLeases(t *testing.T) {
	executor := &DefaultToolExecutor{
		sessionListenGrant: func(_ context.Context, _, _ string) (bool, []uint16) {
			return true, []uint16{8080}
		},
		sessionLoopbackGrant: func(_ context.Context, _, _ string) (bool, []uint16) {
			return true, []uint16{5432}
		},
	}
	tc := ToolContext{PackageExecution: &packageexec.Execution{Manager: "npm"}}
	executor.applySessionListenGrant(t.Context(), &tc)
	executor.applySessionLoopbackGrant(t.Context(), &tc)
	if tc.LocalListenGranted || tc.LoopbackConnectGranted {
		t.Fatalf("remote package execution inherited local network grants: %+v", tc)
	}
}
