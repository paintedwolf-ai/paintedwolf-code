package usernotice

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
)

type proseTurnFixture struct {
	tools  []string
	reason string
}

func (proseTurnFixture) Error() string { return "prose turn" }
func (p proseTurnFixture) NoticeProseTurnToolCalls() ([]string, string) {
	return p.tools, p.reason
}

// Every typed failure contributes trimmed identity fields and omits blank ones.
func TestContextFromPromptErrorTrimsTypedFields(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want map[string]any
	}{
		"thinking override": {&llm.ThinkingOverrideError{ProviderID: " p ", Model: " m ", Reason: " too long "},
			map[string]any{"provider_id": "p", "model": "m", "reason": "too long"}},
		"prose turn": {fmt.Errorf("wrapped: %w", proseTurnFixture{tools: []string{" read ", "write"}, reason: " done "}),
			map[string]any{"tool": "read", "tools": "read , write", "closeout_reason": "done"}},
		"prose turn without tools": {proseTurnFixture{reason: "  "}, map[string]any{}},
		"provider not configured": {&failure.ProviderNotConfiguredError{ProviderID: " openai "},
			map[string]any{"provider_id": "openai"}},
		"provider not configured blank": {&failure.ProviderNotConfiguredError{}, map[string]any{}},
		"context too small": {&failure.ProviderContextTooSmallError{ProviderID: " p ", Model: " m "},
			map[string]any{"provider_id": "p", "model": "m"}},
		"model refused": {&providerretry.ModelRefusedError{ProviderID: "p", Model: "m", Code: " model_not_found "},
			map[string]any{"provider_id": "p", "model": "m", "code": "model_not_found"}},
		"workflow version unavailable": {&runstate.WorkflowVersionUnavailableError{WorkflowID: " plan ", Version: " 2.0.0 "},
			map[string]any{"workflow_id": "plan", "version": "2.0.0"}},
		"host fault": {hostFaultFixture{tool: " write "}, map[string]any{"tool": "write"}},
	} {
		got := ContextFromPromptError(tc.err)
		for key, want := range tc.want {
			if got[key] != want {
				t.Errorf("%s: %s = %#v, want %#v (context %v)", name, key, got[key], want, got)
			}
		}
		if name == "prose turn without tools" || name == "provider not configured blank" {
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s: context = %v, want %v", name, got, tc.want)
			}
		}
	}
}

func TestOutputTruncatedContextNamesTheTruncatedTool(t *testing.T) {
	got := outputTruncatedContext(&failure.ProviderOutputTruncatedError{ToolCallTruncated: true, ToolName: " write ", ToolArgsBytes: 10})
	if got["tool"] != "write" || got["tool_call_truncated"] != true {
		t.Fatalf("context = %v", got)
	}
}
