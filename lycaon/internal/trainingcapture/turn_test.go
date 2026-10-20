package trainingcapture

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCollectionRequiresExplicitConfiguration(t *testing.T) {
	t.Setenv("PW_TRAINING_CAPTURE_DIR", "")
	t.Setenv("PW_TRAINING_EPOCH", "test")
	ctx, capture := Begin(context.Background(), "s", "m", "o")
	req := Prepare(ctx, modelcall.CompletionRequest{})
	if capture != nil || req.ControlCapture != nil {
		t.Fatal("default runtime must not collect training payloads")
	}
	Observe(ctx, nil, false)
	Settled(ctx, "", api.Message{})
	if err := capture.Finish(); err != nil {
		testutil.FailErr(t, "disabled capture", err)
	}
}

func TestTrainingCaptureAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, wire, want              string
		retry, failed, settle, modify bool
	}{
		{name: "exact", wire: `{"messages":[{"role":"user","content":"hello"}]}`, settle: true},
		{name: "retry", wire: `{}`, retry: true, settle: true, want: "ambiguous_dispatch"},
		{name: "unsettled", wire: `{}`, want: "unsettled_output"},
		{name: "failed", wire: `{}`, failed: true, settle: true, want: "host_modified_response"},
		{name: "transformed", wire: `{}`, settle: true, modify: true, want: "host_modified_response"},
		{name: "secret", wire: `{"password":"example-secret"}`, settle: true, want: "secret_screen_changed_payload"},
		{name: "invalid_json", wire: `{`, settle: true, want: "secret_screen_changed_payload"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("PW_TRAINING_CAPTURE_DIR", root)
			t.Setenv("PW_TRAINING_EPOCH", "fixture")
			ctx, capture := Begin(context.Background(), "session", "message", "output")
			req := Prepare(ctx, modelcall.CompletionRequest{Debug: modelcall.RequestDebug{CallID: "call"}})
			req.ControlCapture.Record([]byte(tc.wire))
			if tc.retry {
				req.ControlCapture.Record([]byte(tc.wire))
			}
			Observe(ctx, &modelcall.Completion{Content: "answer"}, tc.failed)
			message := api.Message{Content: "answer", ToolCalls: []api.ToolCall{}}
			if tc.modify {
				message.Content = "changed"
			}
			if tc.settle {
				Settled(ctx, "output", message)
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
			data, err := os.ReadFile(files[0])
			if err != nil {
				testutil.FailErr(t, "read", err)
			}
			var got Record
			if err := json.Unmarshal(data, &got); err != nil {
				testutil.FailErr(t, "decode", err)
			}
			if got.Reason != tc.want {
				t.Fatalf("reason = %q, want %q", got.Reason, tc.want)
			}
			if tc.want != "" && (len(got.Request) != 0 || len(got.Response) != 0) {
				t.Fatal("excluded payload persisted")
			}
			if tc.want == "" && (string(got.Request) != tc.wire || len(got.Response) == 0) {
				t.Fatal("lost exact payload")
			}
			info, err := os.Stat(files[0])
			if err != nil {
				testutil.FailErr(t, "stat", err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatal("capture is not private")
			}
		})
	}
}
