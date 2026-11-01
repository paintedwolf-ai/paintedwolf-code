package toolfeedback

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/capabilityrequest"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestRecoveryContractSurvivesChildContextsAndMetadata(t *testing.T) {
	bp := stockBlockPlane(t)
	for _, capabilities := range []toolcontract.Capability{0, toolcontract.CapabilityLoopbackConnect} {
		ctx := tools.WithRecoveryContract(t.Context(), "command", toolcontract.Contract{Capabilities: capabilities})
		ctx = tools.WithRecoveryContract(ctx, "read", toolcontract.Contract{})
		err := bp.RejectObservation(ctx, "command", "fixture", map[string]any{}, &toolrejection.ToolReject{
			Code: "SANDBOX_CAPABILITY_REQUEST_INVALID", Data: map[string]any{
				"tool": "http_request", "capability_request_fields": []string{"host_execution"},
				"supports_local_listen": true,
			},
		})
		refusal, ok := guidance.RefusalFromError(err)
		if !ok {
			t.Fatalf("missing refusal: %v", err)
		}
		fix := refusal.Copy["fix"]
		if strings.Contains(fix, "host_execution") || strings.Contains(fix, "local_listen") ||
			strings.Contains(fix, "loopback_connect") != (capabilities == toolcontract.CapabilityLoopbackConnect) {
			t.Fatalf("invocation contract changed: %s", fix)
		}
	}
}

func TestTypedCapabilitySnapshotIsLocalToPresentation(t *testing.T) {
	stockBlockPlane(t)
	catalog, err := guidance.LoadHintConfigStock()
	if err != nil {
		t.Fatal(err)
	}
	formatter := guidance.NewStaticRejectFormatter(catalog)
	for _, tool := range []string{"command", "read"} {
		reject := capabilityrequest.ValidateCapabilityContract(tool, toolcontract.Contract{}, map[string]any{
			"capability_request": map[string]any{"direct_ip": map[string]any{}},
		})
		if reject == nil {
			t.Fatal("unsupported capability accepted")
		}
		reject.Data["tool"] = "command"
		before, err := json.Marshal(reject)
		if err != nil {
			t.Fatal(err)
		}
		rendered := toolrejection.RenderReject(reject, formatter)
		after, err := json.Marshal(reject)
		if err != nil || string(before) != string(after) || strings.Contains(string(after), "CapabilityContract") || strings.Contains(string(after), "_host_capability_snapshot") {
			t.Fatalf("presentation mutated canonical rejection: %s; %v", after, err)
		}
		text := rendered.Error()
		if strings.Contains(text, "host_execution") || strings.Contains(text, "loopback_connect") ||
			strings.Contains(text, "Remove capability_request") != (tool == "command") {
			t.Fatalf("typed or foreign contract widened recovery: %s", text)
		}
	}
}
