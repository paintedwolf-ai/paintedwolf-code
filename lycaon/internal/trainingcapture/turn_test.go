package trainingcapture

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func configure(t *testing.T, root string) {
	t.Helper()
	t.Setenv("PW_TRAINING_CAPTURE_DIR", root)
	t.Setenv("PW_TRAINING_EPOCH", "fixture")
	t.Setenv("PW_TRAINING_STUDENT_PROVIDER", "student")
	t.Setenv("PW_TRAINING_STUDENT_MODEL", "student-e4b")
}

func TestCollectionRequiresExplicitConfiguration(t *testing.T) {
	configure(t, t.TempDir())
	t.Setenv("PW_TRAINING_STUDENT_PROVIDER", "")
	ctx, capture := Begin(context.Background(), "s", "m", "o")
	req := Prepare(ctx, modelcall.CompletionRequest{}, Policy{})
	if capture != nil || req.ControlCapture != nil {
		t.Fatal("a collection without a declared student must not collect")
	}
	Observe(ctx, nil, false, "", "")
	Settled(ctx, "", api.Message{})
	TurnCommitted(ctx, "s", "m")
	if err := capture.Finish(); err != nil {
		testutil.FailErr(t, "disabled capture", err)
	}
}

// fixturePolicy flags messages carrying "MANAGED-SECRET" and renders the
// screened request as the student provider would, tagging it so a test can
// tell the training input from the teacher dispatch.
func fixturePolicy(renderErr error) Policy {
	return Policy{
		StorageChanges: func(msg api.Message) bool { return strings.Contains(msg.Content, "MANAGED-SECRET") },
		Render: func(_ context.Context, provider, model string, req modelcall.CompletionRequest) (string, []byte, error) {
			if renderErr != nil {
				return "", nil, renderErr
			}
			body, err := json.Marshal(map[string]any{"rendered_for": provider + "/" + model, "messages": len(req.Messages)})
			return "ollama", body, err
		},
	}
}

type admission struct {
	name, want    string
	messages      []api.Message
	retry, failed bool
	settle        bool
	raw, settled  []api.ToolCall
	content       string
	policy        *Policy
	noSource      bool
}

func readCapture(t *testing.T, tc admission) Record {
	t.Helper()
	root := t.TempDir()
	configure(t, root)
	ctx, capture := Begin(context.Background(), "session", "message", "output")
	policy := fixturePolicy(nil)
	if tc.policy != nil {
		policy = *tc.policy
	}
	messages := tc.messages
	if messages == nil {
		messages = []api.Message{{Role: api.MessageRoleUser, Content: "hello"}}
	}
	req := Prepare(ctx, modelcall.CompletionRequest{Messages: messages, Debug: modelcall.RequestDebug{CallID: "call"}}, policy)
	dispatch := func() {
		if tc.noSource {
			req.ControlCapture.Record([]byte(`{"teacher":true}`))
			return
		}
		req.ControlCapture.RecordDispatch(req, []byte(`{"teacher":true}`))
	}
	dispatch()
	if tc.retry {
		dispatch()
	}
	Observe(ctx, &modelcall.Completion{Content: "answer", ToolCalls: tc.raw}, tc.failed, "fireworks-deepseek", "deepseek-v4p1-flash")
	content := tc.content
	if content == "" {
		content = "answer"
	}
	if tc.settle {
		Settled(ctx, "output", api.Message{Content: content, ToolCalls: tc.settled})
	}
	if err := capture.Finish(); err != nil {
		testutil.FailErr(t, "finish", err)
	}
	files, err := filepath.Glob(filepath.Join(root, "*.json"))
	if err != nil {
		testutil.FailErr(t, "list", err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %v", files)
	}
	info, err := os.Stat(files[0])
	if err != nil {
		testutil.FailErr(t, "stat", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatal("capture is not private")
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		testutil.FailErr(t, "read", err)
	}
	var got Record
	testutil.FailErr(t, "decode", json.Unmarshal(data, &got))
	if got.Reason != tc.want {
		t.Fatalf("reason = %q, want %q", got.Reason, tc.want)
	}
	if tc.want != "" && (len(got.Request) != 0 || len(got.StudentRequest) != 0 || len(got.Response) != 0) {
		t.Fatal("excluded payload persisted")
	}
	if tc.want == "" {
		if string(got.Request) != `{"teacher":true}` || len(got.Response) == 0 {
			t.Fatal("lost the exact teacher dispatch or the target")
		}
		if !strings.Contains(string(got.StudentRequest), `"rendered_for":"student/student-e4b"`) || got.StudentKind != "ollama" {
			t.Fatalf("student request = %s (%s)", got.StudentRequest, got.StudentKind)
		}
	}
	if got.Provider != "fireworks-deepseek" || got.Model != "deepseek-v4p1-flash" {
		t.Fatalf("dispatch identity = %q/%q", got.Provider, got.Model)
	}
	return got
}

func TestTrainingCaptureAdmission(t *testing.T) {
	missing := Policy{StorageChanges: fixturePolicy(nil).StorageChanges}
	unservable := fixturePolicy(errors.New("prompt exceeds the student's window"))
	for _, tc := range []admission{
		{name: "exact", settle: true},
		{name: "retry", retry: true, settle: true, want: "ambiguous_dispatch"},
		{name: "unsettled", want: "unsettled_output"},
		{name: "failed_stays_failed", failed: true, settle: true, want: "provider_failed"},
		{name: "content_changed", settle: true, content: "changed", want: "host_modified_response"},
		{name: "durable_policy", settle: true, want: "transcript_policy_changes_request",
			messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}, {Role: api.MessageRoleTool, Content: "MANAGED-SECRET value"}}},
		{name: "student_cannot_be_served", settle: true, policy: &unservable, want: "student_cannot_be_served"},
		{name: "no_renderer", settle: true, policy: &missing, want: "collection_policy_unavailable"},
		{name: "adapter_recorded_no_source", settle: true, noSource: true, want: "dispatch_source_unrecorded"},
	} {
		t.Run(tc.name, func(t *testing.T) { readCapture(t, tc) })
	}
}

func TestPolicyFindingNamesTheMessage(t *testing.T) {
	got := readCapture(t, admission{settle: true, want: "transcript_policy_changes_request",
		messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}, {Role: api.MessageRoleTool, Content: "MANAGED-SECRET"}}})
	if got.PolicyLocation != "messages[1]" {
		t.Fatalf("location = %q", got.PolicyLocation)
	}
}

func TestHostNormalizationBecomesTheTarget(t *testing.T) {
	read := api.ToolCall{ID: "provider-1", Name: "read", Args: map[string]any{"path": "a.go"}}
	settled := api.ToolCall{ID: "host-1", Name: "read", Args: map[string]any{"path": "a.go"}, DisplayTitle: "a.go"}
	got := readCapture(t, admission{settle: true, raw: []api.ToolCall{read, read}, settled: []api.ToolCall{settled}})
	if !slices.Equal(got.Normalized, []string{"dropped_calls", "call_ids"}) || len(got.ModifiedFields) != 0 {
		t.Fatalf("normalized = %v, modified = %v", got.Normalized, got.ModifiedFields)
	}
	var target response
	testutil.FailErr(t, "decode target", json.Unmarshal(got.Response, &target))
	if len(target.Calls) != 1 || target.Calls[0].ID != "host-1" {
		t.Fatalf("target calls = %+v, want the settled call the receipt names", target.Calls)
	}
}

func TestChangedArgumentsExcludeTheTurn(t *testing.T) {
	raw := api.ToolCall{ID: "c", Name: "read", Args: map[string]any{"path": "a.go"}}
	changed := api.ToolCall{ID: "c", Name: "read", Args: map[string]any{"path": "b.go"}}
	got := readCapture(t, admission{settle: true, raw: []api.ToolCall{raw}, settled: []api.ToolCall{changed}, want: "host_modified_response"})
	if !slices.Equal(got.ModifiedFields, []string{"tool_calls"}) {
		t.Fatalf("modified = %v", got.ModifiedFields)
	}
}

func TestTurnCommittedWritesAHostFact(t *testing.T) {
	root := t.TempDir()
	configure(t, root)
	TurnCommitted(context.Background(), "session", "message")
	files, err := filepath.Glob(filepath.Join(root, "*.json"))
	testutil.FailErr(t, "list", err)
	if len(files) != 1 {
		t.Fatalf("files = %v", files)
	}
	data, err := os.ReadFile(files[0])
	testutil.FailErr(t, "read", err)
	var fact Fact
	testutil.FailErr(t, "decode", json.Unmarshal(data, &fact))
	if fact.Schema != "pw-training-fact/1" || fact.Kind != "turn_committed" || fact.SessionID != "session" || fact.MessageID != "message" || fact.DecidedAt == "" {
		t.Fatalf("fact = %+v", fact)
	}
}
