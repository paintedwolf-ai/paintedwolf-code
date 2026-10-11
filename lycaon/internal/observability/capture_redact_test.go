package observability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// dotEnvToolResult is a synthetic environment-file tool result.
const dotEnvToolResult = `1	DATA_DIR=/tmp/data
2	MARGINALIA_API_KEY="POL-NC-0000000-0000-0000-0000-000000000000"
3	BRAVE_SEARCH_API_KEY="BSAtesttesttesttesttesttestte"
4	GITHUB_TOKEN="ghp_000000000000000000000000000000000000"
5	VULNERS_API_KEY="AAAABBBBCCCCDDDDEEEEFFFFGGGGHHHHIIIIJJJJKKKKLLLLMMMMNNNNOOOOPPPP"
6	NVD_API_KEY="00000000-0000-4000-8000-000000000000"
7	ABUSE_CH_AUTH_KEY="0000000000000000000000000000000000000000000000"
`

// secretValues are the protected substrings in dotEnvToolResult.
var secretValues = []string{
	"POL-NC-0000000-0000-0000-0000-000000000000",
	"BSAtesttesttesttesttesttestte",
	"ghp_000000000000000000000000000000000000",
	"AAAABBBBCCCCDDDDEEEEFFFFGGGGHHHHIIIIJJJJKKKKLLLLMMMMNNNNOOOOPPPP",
	"00000000-0000-4000-8000-000000000000",
	"0000000000000000000000000000000000000000000000",
}

func TestRedactMessagesForCaptureStripsEnvAssignments(t *testing.T) {
	msgs := []api.Message{{
		Role:    api.MessageRoleTool,
		Content: dotEnvToolResult,
	}}

	out := RedactMessagesForCapture(msgs)

	for _, secret := range secretValues {
		if strings.Contains(out[0].Content, secret) {
			t.Fatalf("capture kept a credential value: %q still present", secret)
		}
	}
	// Redaction preserves surrounding diagnostic context.
	if !strings.Contains(out[0].Content, "MARGINALIA_API_KEY") {
		t.Fatalf("redaction dropped the key name as well as the value: %q", out[0].Content)
	}
	if !strings.Contains(out[0].Content, "DATA_DIR=/tmp/data") {
		t.Fatalf("redaction ate a non-secret assignment: %q", out[0].Content)
	}
}

func TestCatalogCaptureRedactorDoesNotBypassFieldFloor(t *testing.T) {
	const credential = "opaque-new-credential"
	got := redactCaptureText(`{"secret_value":"`+credential+`","name":"useful context"}`, func(s string) string {
		return s
	})
	if strings.Contains(got, credential) {
		t.Fatalf("catalog redactor bypassed the field floor: %s", got)
	}
	if !strings.Contains(got, "useful context") {
		t.Fatalf("field floor removed non-secret context: %s", got)
	}
}

func TestRedactMessagesForCaptureLeavesCallerHistoryIntact(t *testing.T) {
	msgs := []api.Message{{
		Role:         api.MessageRoleTool,
		Content:      dotEnvToolResult,
		ContentParts: []api.MessageContentPart{{Content: dotEnvToolResult}},
		ToolResult:   &api.ToolResult{Content: dotEnvToolResult},
		ToolCalls: []api.ToolCall{{
			Name: "write",
			Args: map[string]any{"api_key": "POL-NC-0000000-0000-0000-0000-000000000000"},
		}},
	}}

	_ = RedactMessagesForCapture(msgs)

	// Capture redaction does not mutate model-facing history.
	if !strings.Contains(msgs[0].Content, secretValues[0]) {
		t.Fatal("RedactMessagesForCapture mutated the caller's message content")
	}
	if !strings.Contains(msgs[0].ContentParts[0].Content, secretValues[0]) {
		t.Fatal("RedactMessagesForCapture mutated the caller's content parts")
	}
	if !strings.Contains(msgs[0].ToolResult.Content, secretValues[0]) {
		t.Fatal("RedactMessagesForCapture mutated the caller's tool result")
	}
	if msgs[0].ToolCalls[0].Args["api_key"] != secretValues[0] {
		t.Fatal("RedactMessagesForCapture mutated the caller's tool-call args")
	}
}

func TestRedactMessagesForCaptureScrubsNestedFields(t *testing.T) {
	SetCaptureRedactor(func(value string) string {
		return strings.ReplaceAll(value, secretValues[0], "[REDACTED]")
	})
	t.Cleanup(func() { SetCaptureRedactor(nil) })
	before := "before " + secretValues[0]
	msgs := []api.Message{{
		Role:             api.MessageRoleAssistant,
		ContentParts:     []api.MessageContentPart{{Content: dotEnvToolResult}},
		WorkflowBoundary: &api.WorkflowBoundaryMeta{Reason: "reason " + secretValues[0]},
		ProgressUpdate:   &api.ProgressUpdateMeta{Steps: []api.ProgressStep{{Label: "step " + secretValues[0]}}},
		ToolResult: &api.ToolResult{
			Content:  dotEnvToolResult,
			Feedback: []api.ToolFeedback{{Details: map[string]any{"note": secretValues[0]}}},
			FileEdit: &api.FileEditSnapshot{Path: "path-" + secretValues[0], Before: &before, After: secretValues[0]},
			Skill:    &api.SkillActivation{Instructions: "use " + secretValues[0]},
		},
		ToolCalls: []api.ToolCall{{
			Name: "write",
			Args: map[string]any{
				"path":    "config.yaml",
				"api_key": secretValues[0],
				"nested":  map[string]any{"auth_key": secretValues[5]},
			},
		}},
	}}

	out := RedactMessagesForCapture(msgs)

	if strings.Contains(out[0].ContentParts[0].Content, secretValues[0]) {
		t.Fatal("content parts were not scrubbed")
	}
	if strings.Contains(out[0].ToolResult.Content, secretValues[0]) {
		t.Fatal("tool result was not scrubbed")
	}
	args := out[0].ToolCalls[0].Args
	if args["api_key"] == secretValues[0] {
		t.Fatal("tool-call args were not scrubbed")
	}
	nested, ok := args["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested args lost their shape: %#v", args["nested"])
	}
	if nested["auth_key"] == secretValues[5] {
		t.Fatal("nested secret-named arg was not scrubbed")
	}
	if args["path"] != "config.yaml" {
		t.Fatalf("scrub rewrote a non-secret arg: %#v", args["path"])
	}
	raw, err := json.Marshal(out[0])
	testutil.FailErr(t, "marshal capture projection", err)
	if strings.Contains(string(raw), secretValues[0]) {
		t.Fatalf("structured capture retained a credential: %s", raw)
	}
}

// TestLogLLMRequestWritesNoCredentials covers the capture-file boundary.
func TestLogLLMRequestWritesNoCredentials(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.jsonl")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", logPath)
	CloseLLMDebug()

	LogLLMRequest("mock", "mock", "stream", []api.Message{{
		Role:    api.MessageRoleTool,
		Content: dotEnvToolResult,
	}}, nil, LLMRequestTiming{}, LLMUsageCapture{PromptTokens: 1, CompletionTokens: 1},
		LLMRequestDebug{SessionID: "sess-1"},
		&LLMCompletionCapture{ContentPreview: dotEnvToolResult}, "")

	data, err := os.ReadFile(logPath)
	testutil.FailErr(t, "read capture", err)

	for _, secret := range secretValues {
		if strings.Contains(string(data), secret) {
			t.Fatalf("capture file contains credential %q", secret)
		}
	}

	var entry llmCaptureEntry
	testutil.FailErr(t, "unmarshal", json.Unmarshal(data[:len(data)-1], &entry))
	if len(entry.Messages) != 1 {
		t.Fatalf("messages = %+v", entry.Messages)
	}
	if entry.Completion == nil || strings.Contains(entry.Completion.ContentPreview, secretValues[0]) {
		t.Fatalf("completion preview kept a credential: %+v", entry.Completion)
	}
}

// TestLogLLMRequestRedactsToolDefinitions covers the schema half of the record.
// The host treats a tool definition as a credential source: the model screen
// scans and redacts Description and ArgsSchema before a provider sees them, so
// an MCP schema carrying a token in a default cannot land raw in the file users
// attach to bug reports.
func TestLogLLMRequestRedactsToolDefinitions(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.jsonl")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", logPath)
	CloseLLMDebug()

	const describedToken = "ghp_111111111111111111111111111111111111"
	const schemaToken = "opaque-relay-handle-2222"
	// The runtime catalog is what recognizes an opaque value; the floor covers
	// the shaped one. Both reach the capture through the same call.
	SetCaptureRedactor(func(s string) string { return strings.ReplaceAll(s, schemaToken, "[REDACTED]") })
	t.Cleanup(func() { SetCaptureRedactor(nil) })

	LogLLMRequest("mock", "mock", "stream", nil, []LLMToolCapture{{
		Name:        "relay_send",
		Description: "Call the relay. Example: Bearer " + describedToken,
		ArgsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"handle": map[string]any{"type": "string", "default": schemaToken},
			},
		},
	}}, LLMRequestTiming{}, LLMUsageCapture{}, LLMRequestDebug{SessionID: "sess-tools"}, nil, "")

	data, err := os.ReadFile(logPath)
	testutil.FailErr(t, "read capture", err)
	for _, secret := range []string{describedToken, schemaToken} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("capture file contains tool-definition credential %q: %s", secret, data)
		}
	}

	var entry llmCaptureEntry
	testutil.FailErr(t, "unmarshal", json.Unmarshal(data[:len(data)-1], &entry))
	if len(entry.Tools) != 1 || entry.Tools[0].Name != "relay_send" {
		t.Fatalf("tool capture lost its identity: %+v", entry.Tools)
	}
	if !strings.Contains(entry.Tools[0].Description, "Call the relay.") {
		t.Fatalf("redaction ate the whole description: %q", entry.Tools[0].Description)
	}
	properties, ok := entry.Tools[0].ArgsSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("redaction dropped the schema shape: %+v", entry.Tools[0].ArgsSchema)
	}
	handle, ok := properties["handle"].(map[string]any)
	if !ok || handle["type"] != "string" {
		t.Fatalf("redaction dropped a schema property: %+v", properties)
	}
}

// TestSessionManifestWritesNoCredentials covers the derived task line.
func TestSessionManifestWritesNoCredentials(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.jsonl")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", logPath)
	CloseLLMDebug()

	LogLLMRequest("mock", "mock", "stream", []api.Message{{
		Role:    api.MessageRoleUser,
		Content: `deploy with API_KEY="` + secretValues[0] + `"`,
	}}, nil, LLMRequestTiming{}, LLMUsageCapture{PromptTokens: 1},
		LLMRequestDebug{SessionID: "sess-manifest"}, nil, "")

	data, err := os.ReadFile(filepath.Join(dir, debugpaths.Name(debugpaths.KindSessions)))
	testutil.FailErr(t, "read manifest", err)
	if strings.Contains(string(data), secretValues[0]) {
		t.Fatalf("session manifest contains a credential: %s", data)
	}
}

// Name-anchored redaction leaves unshaped values in prose unchanged.
func TestCaptureRedactionBoundaryIsNameAnchored(t *testing.T) {
	bare := "POL-NC-0000000-0000-0000-0000-000000000000"

	if got := RedactCaptureText("deploy with " + bare); !strings.Contains(got, bare) {
		t.Fatalf("unnamed opaque value was redacted — the boundary moved, update this test and the docs: %q", got)
	}
	if got := RedactCaptureText(`API_KEY="` + bare + `"`); strings.Contains(got, bare) {
		t.Fatalf("named assignment was not redacted: %q", got)
	}
	// Published shapes do not require a field name.
	shaped := "ghp_000000000000000000000000000000000000"
	if got := RedactCaptureText("Bearer " + shaped); strings.Contains(got, shaped) {
		t.Fatalf("bearer token was not redacted: %q", got)
	}
}

func TestCaptureRedactorReleasePreservesReplacementAndFieldFloor(t *testing.T) {
	releaseOld := SetCaptureRedactor(func(value string) string { return strings.ReplaceAll(value, "old-marker", "old-screened") })
	releaseCurrent := SetCaptureRedactor(func(value string) string { return strings.ReplaceAll(value, "current-marker", "current-screened") })
	defer releaseOld()
	defer releaseCurrent()
	releaseOld()
	releaseOld()
	if got := RedactCaptureText("current-marker"); got != "current-screened" {
		t.Fatalf("old owner removed current redaction: %q", got)
	}
	releaseCurrent()
	if got := RedactCaptureText("current-marker"); got != "current-marker" {
		t.Fatalf("released owner remains installed: %q", got)
	}
	if got := RedactCaptureText(`{"api_key":"credential-plain"}`); strings.Contains(got, "credential-plain") {
		t.Fatal("released catalog disabled the capture field floor")
	}
}
