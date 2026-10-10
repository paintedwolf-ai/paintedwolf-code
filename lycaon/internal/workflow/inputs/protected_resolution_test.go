package inputs_test

import (
	"context"
	"encoding/json"
	"errors"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowinputs "github.com/lycaon/lycaon/internal/workflow/inputs"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"strings"
	"testing"
)

func TestSecretResolutionRetainsOnlyReferenceAndRollsBackFailedCapture(t *testing.T) {
	for _, mode := range []string{"success", "capture error", "empty reference"} {
		t.Run(mode, func(t *testing.T) {
			mgr, store, _, _ := testManagerWithRegistry(t)
			mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"ask-user-host@1.0.0": askUserTestManifest()})
			ctx := workflowCaller(t, mgr)
			run, err := startRun(ctx, mgr, "sess-1", "ask-user-host", "1.0.0")
			if err != nil {
				t.Fatalf("operation failed: %v", err)
			}
			ask, err := mgr.Asks.RequestUserInput(ctx, "sess-1", workflowinputs.UserInputRequest{Prompt: "Provide registry key", ResponseType: workflowdef.FeedbackResponseSecret, Secret: &workflowdef.SecretInputSpec{Name: "Registry key", Purpose: "Publish artifact", Scope: "chat"}, ToolCallID: "capture-call"})
			if err != nil {
				t.Fatalf("operation failed: %v", err)
			}
			const raw = "protected-response-never-persisted"
			const ref = "{{paintedwolf-secret:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa}}"
			discarded := 0
			mgr.Asks.SetSecretCapture(func(_ context.Context, req workflowinputs.SecretCaptureRequest) (workflowinputs.SecretCaptureResult, error) {
				if req.Value != raw || req.SessionID != "sess-1" || req.PersonID == "" || req.OperationID != "ask_user:"+ask.PhaseID {
					t.Fatalf("capture identity=%+v", req)
				}
				result := workflowinputs.SecretCaptureResult{Reference: ref, Discard: func(context.Context) error { discarded++; return nil }}
				if mode == "capture error" {
					return result, errors.New("storage refused")
				}
				if mode == "empty reference" {
					result.Reference = ""
				}
				return result, nil
			})
			_, err = mgr.Asks.ResolveUserSecret(ctx, "sess-1", run.ID, ask.PhaseID, raw)
			if (err == nil) != (mode == "success") {
				t.Fatalf("resolution error=%v", err)
			}
			vars, readErr := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
			if readErr != nil {
				t.Fatalf("read retained workflow variables: %v", readErr)
			}
			msgs, readErr := store.GetMessages(ctx, "sess-1")
			if readErr != nil {
				t.Fatalf("read retained transcript: %v", readErr)
			}
			for _, value := range []any{vars, msgs} {
				encoded, e := json.Marshal(value)
				if e != nil {
					t.Fatalf("encode retained state: %v", e)
				}
				if strings.Contains(string(encoded), raw) {
					t.Fatal("raw response persisted")
				}
			}
			pending, isPending := runstate.CoordinatorAskPendingFromVars(vars)
			if mode == "success" {
				if isPending || discarded != 0 {
					t.Fatalf("successful capture pending=%v discards=%d", isPending, discarded)
				}
				encoded, _ := json.Marshal(vars)
				if !strings.Contains(string(encoded), ref) {
					t.Fatal("retained secret reference missing")
				}
			} else if !isPending || pending.ID != ask.PhaseID || discarded != 1 {
				t.Fatalf("failed capture changed pending ask=%+v pending=%v discards=%d", pending, isPending, discarded)
			}
		})
	}
}
