package toolfeedback

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"testing"
)

func TestCommandSurfaceRejectsRenderGuidance(t *testing.T) {
	bp := stockBlockPlane(t)
	cases := []struct {
		tool   string
		reject *toolrejection.ToolReject
		want   []string
	}{
		{
			tool: "command",
			reject: &toolrejection.ToolReject{Code: "COMMAND_FAILURE_LOOP", Data: map[string]any{
				"command": "cargo test", "count": 3,
			}},
			want: []string{"cargo test", "failed 3 times", "change the command"},
		},
		{
			tool: "command",
			reject: &toolrejection.ToolReject{Code: "COMMAND_NOT_FOUND", Data: map[string]any{
				"command": "missing-tool",
			}},
			want: []string{"missing-tool", "PATH"},
		},
		{
			tool: "command",
			reject: &toolrejection.ToolReject{Code: "COMMAND_PWD_NOT_CWD", Data: map[string]any{
				"command": "go mod tidy", "cwd": "/project", "pwd": "/project/subdir",
			}},
			want: []string{"env.PWD", "/project/subdir", "cwd:"},
		},
		{
			tool: "command",
			reject: &toolrejection.ToolReject{Code: "COMMAND_IN_FLIGHT", Data: map[string]any{
				"handles": []string{"command-1"}, "handles_text": "command-1",
			}},
			want: []string{"command-1", "allow_concurrent"},
		},
		{
			tool: "command",
			reject: &toolrejection.ToolReject{Code: "COMMAND_DUPLICATE_RUNNING", Data: map[string]any{
				"handles": []string{"command-1"}, "handles_text": "command-1",
			}},
			want: []string{"command-1", "command_output"},
		},
		{
			tool:   "command",
			reject: &toolrejection.ToolReject{Code: "COMMAND_CONCURRENCY_CAP_REACHED", Data: map[string]any{"max_awaited": 4, "count": 4, "live_command_handles": []string{"awaited-command-7"}}},
			want:   []string{"4", "awaited-command-7"},
		},
		{
			tool:   "wait",
			reject: &toolrejection.ToolReject{Code: "WAIT_CONDITION_NOT_ALLOWED", Data: map[string]any{"condition": "process_done", "profile": "worker_readonly", "wait_allowed_conditions": []string{"timer"}}},
			want:   []string{"process_done", "worker_readonly", "timer"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.reject.Code, func(t *testing.T) {
			err := bp.RejectObservation(context.Background(), tc.tool, "implement", nil, tc.reject)
			if err == nil {
				t.Fatalf("%s produced no Decision — the agent would receive the bare code", tc.reject.Code)
			}
			msg := err.Error()
			if msg == tc.reject.Code {
				t.Fatalf("%s rendered as its own bare code", tc.reject.Code)
			}
			for _, want := range append([]string{"Rejected:", "Code: " + tc.reject.Code}, tc.want...) {
				if !strings.Contains(msg, want) {
					t.Fatalf("%s reject missing %q in:\n%s", tc.reject.Code, want, msg)
				}
			}
			if strings.Contains(msg, "{{") || strings.Contains(msg, "{%") {
				t.Fatalf("%s left template syntax unrendered:\n%s", tc.reject.Code, msg)
			}
		})
	}
}

// Write-scope rejections outrank sandbox capability guidance.
func TestWriteScopeRedirectNotStolenBySandboxCapability(t *testing.T) {
	bp := stockBlockPlane(t)
	err := bp.RejectObservation(context.Background(), "command", "coordinator", map[string]any{
		"command": "python3", "stdout_to": "detection-packs-catalog.json",
	}, &toolrejection.ToolReject{
		Code: "WRITE_SCOPE_DENIED",
		Data: map[string]any{
			"kind":          "redirect",
			"path":          "detection-packs-catalog.json",
			"tool":          "command",
			"profile":       "coordinator",
			"patterns_list": "- `.paintedwolf/blueprints/**`",
		},
	})
	if err == nil {
		t.Fatal("expected WRITE_SCOPE_DENIED Decision")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Code: WRITE_SCOPE_DENIED") {
		t.Fatalf("want WRITE_SCOPE_DENIED, got:\n%s", msg)
	}
	if strings.Contains(msg, "SANDBOX_") {
		t.Fatalf("WRITE_SCOPE_DENIED was stolen by a SANDBOX_* unit:\n%s", msg)
	}
	if !strings.Contains(msg, "stdout_to") && !strings.Contains(msg, "redirect") && !strings.Contains(msg, "tail") {
		t.Fatalf("want redirect/write-scope guidance, got:\n%s", msg)
	}
}

func TestSandboxCapabilityRejectsRenderGuidance(t *testing.T) {
	bp := stockBlockPlane(t)
	codes := []string{
		"SANDBOX_APPROVAL_UNAVAILABLE",
		"SANDBOX_CAPABILITY_REQUEST_INVALID",
		"SANDBOX_CONTROL_PLANE_DENIED",
		"SANDBOX_DIRECT_IP_AUTHORIZATION_CHANGED",
		"SANDBOX_DIRECT_IP_REQUEST_INVALID",
		"SANDBOX_SOCKET_PATH_CHANGED",
		"SANDBOX_SOCKET_PATH_INVALID",
		"SANDBOX_SOCKET_PATH_LIMIT",
		"SANDBOX_SOCKET_PATH_NOT_FOUND",
		"SANDBOX_SOCKET_PATH_NOT_SOCKET",
		"SANDBOX_SOCKET_PATH_REFUSED",
	}
	for _, code := range codes {
		t.Run(code, func(t *testing.T) {
			err := bp.RejectObservation(context.Background(), "command", "implement", nil, &toolrejection.ToolReject{Code: code})
			if err == nil {
				t.Fatalf("%s produced no Decision — the agent would receive the bare code", code)
			}
			msg := err.Error()
			if msg == code {
				t.Fatalf("%s rendered as its own bare code", code)
			}
			if !strings.Contains(msg, "Rejected:") || !strings.Contains(msg, "Code: "+code) {
				t.Fatalf("%s reject missing envelope:\n%s", code, msg)
			}
			if strings.Contains(msg, "{{") || strings.Contains(msg, "{%") {
				t.Fatalf("%s left template syntax unrendered:\n%s", code, msg)
			}
		})
	}
}
