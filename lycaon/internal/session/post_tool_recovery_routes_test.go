package session

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSandboxRecoveryRoutesDeclaredCapabilityFacts(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(t.TempDir(), "config"))
	mgr := newPostToolGuidanceManager(t)
	for _, tc := range []struct {
		name, code, capability string
		refusal                confine.SandboxRefusal
	}{
		{"protected read", "SANDBOX_TRY_READ_PATH", "read_path", confine.SandboxRefusal{Operation: "file-read-data", Target: "/protected/input", Grant: "/protected/input", Layer: confine.FloorReadKeyMaterial, Recovery: confine.RecoverReadPath}},
		{"credential store", "SANDBOX_TRY_CREDENTIAL_STORE", "credential_store", confine.SandboxRefusal{Operation: "file-read-data", Target: "/Users/person/Library/Keychains/login.keychain-db", Grant: "/Users/person/Library/Keychains/login.keychain-db", Layer: confine.FloorReadCredentialStore, Recovery: confine.RecoverCredentialStore}},
		{"outside write", "SANDBOX_TRY_WRITE_ROOT", "write_root", confine.SandboxRefusal{Operation: "file-write-data", Target: "/outside/output", Grant: "/outside", Layer: confine.FloorOutsideWriteRoots, Recovery: confine.RecoverWriteRoot}},
		{"socket", "SANDBOX_TRY_SOCKET_PATH", "socket_paths", confine.SandboxRefusal{Operation: "network-outbound", Target: "/tmp/service.sock", Grant: "/tmp/service.sock", Recovery: confine.RecoverSocketPath}},
		{"listener", "SANDBOX_TRY_LOCAL_NETWORK", "local_listen", confine.SandboxRefusal{Operation: "network-bind", Target: "local:*:8080", Recovery: confine.RecoverLocalListen}},
		{"outbound port without address", "SANDBOX_TRY_LOCAL_NETWORK", "", confine.SandboxRefusal{Operation: "network-outbound", Target: "remote:*:5432", Recovery: confine.RecoverOutboundPort}},
		{"process control", "SANDBOX_TRY_PROCESS_CONTROL", "process_control", confine.SandboxRefusal{Operation: "signal", Target: "4121", Recovery: confine.RecoverProcessControl}},
		{"host execution", "SANDBOX_TRY_HOST_EXECUTION", "host_execution", confine.SandboxRefusal{Operation: "iokit-open", Target: "AppleUSBHostDevice", Recovery: confine.RecoverHostExecution}},
	} {
		for _, tool := range []string{"command", "verify"} {
			for _, state := range []string{"running", "failed", "succeeded"} {
				t.Run(fmt.Sprintf("%s/%s/%s", tc.name, tool, state), func(t *testing.T) {
					obs := confine.Observation{Applied: true, Running: state == "running", Refusals: confine.SandboxRefusals{
						Witness: confine.WitnessKernel, Refusals: []confine.SandboxRefusal{tc.refusal},
					}}
					if state == "failed" {
						obs.FailedStages = []string{"fixture"}
					}
					out, facts := mgr.ToolPolicy.AfterTool(tools.WithRecoveryTools(t.Context(), []string{tool}), &api.Session{ID: t.Name()}, tool, map[string]any{}, "{}", 1, guidance.ToolResultFacts{Confine: obs})
					if state == "succeeded" {
						if facts.HasCode(tc.code) {
							t.Fatalf("absorbed refusal acquired retry guidance: %s", out)
						}
						return
					}
					if !facts.HasCode(tc.code) {
						t.Fatalf("typed refusal lost route %s: %s", tc.code, out)
					}
					contract, known := toolcontract.Lookup(tool)
					canRequest := tc.capability != "" && known && slices.Contains(contract.CapabilityRequestFields(), tc.capability)
					if state == "failed" && !canRequest && strings.Contains(out, "Fix:") {
						t.Fatalf("unsupported or ungrounded request acquired a recipe: %s", out)
					}
					if canRequest && (!strings.Contains(out, "capability_request."+tc.capability) || !strings.Contains(out, "Preserve completed effects")) {
						t.Fatalf("known missing capability lost bounded recovery: %s", out)
					}
					if tc.capability == "" && strings.Contains(out, "capability_request.loopback_connect") {
						t.Fatalf("port-only observation invented a loopback address grant: %s", out)
					}
					if state == "running" && !strings.Contains(out, "Do not start replacement work while this job is running") {
						t.Fatalf("recovery bypassed the running job: %s", out)
					}
				})
			}
		}
	}
}

func TestSandboxRecoveryPreservesRunningJobDependency(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	for offered := 0; offered < 4; offered++ {
		t.Run(fmt.Sprint(offered), func(t *testing.T) {
			names := []string{"command"}
			for _, control := range []struct {
				name string
				mask int
			}{{"command_stop", 1}, {"command_output", 2}} {
				if offered&control.mask != 0 {
					names = append(names, control.name)
				}
			}
			obs := confine.Observation{Applied: true, Running: true, Refusals: confine.SandboxRefusals{
				Witness: confine.WitnessKernel, Refusals: []confine.SandboxRefusal{{Operation: "file-read-data", Target: "/protected/input", Grant: "/protected/input", Layer: confine.FloorReadKeyMaterial, Recovery: confine.RecoverReadPath}},
			}}
			ctx := tools.WithRecoveryTools(t.Context(), names)
			out, facts := mgr.ToolPolicy.AfterTool(ctx, &api.Session{ID: t.Name()}, "command", map[string]any{}, "{}", 1, guidance.ToolResultFacts{Confine: obs})
			if !facts.HasCode("SANDBOX_TRY_READ_PATH") || !strings.Contains(out, "Do not start replacement work while this job is running") {
				t.Fatalf("running refusal lost its dependency: %s", out)
			}
			for _, control := range []string{"command_stop", "command_output"} {
				if strings.Contains(out, control) != tools.ToolOffered(ctx, control) {
					t.Errorf("control %s contradicts frozen offered schemas: %s", control, out)
				}
			}
			if tools.ToolOffered(ctx, "command_stop") && !strings.Contains(out, "observe its exit") {
				t.Fatalf("stop acknowledgement replaced observed exit: %s", out)
			}
		})
	}
}
