package inputs_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/hostctx"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/naming"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

type observingStartAdmission struct{ active bool }

func (a *observingStartAdmission) WithSessionTreeAdmission(_ context.Context, _ string, fn func() error) error {
	a.active = true
	defer func() { a.active = false }()
	return fn()
}

func TestWorkflowRequestsCurateRootChatAfterAdmission(t *testing.T) {
	for _, surface := range []string{"slash", "human start", "API start", "default", "answer", "manual title"} {
		t.Run(surface, func(t *testing.T) {
			t.Setenv("LYCAON_LLM_MOCK", "1")
			mgr, sessions, _, _ := testManager(t)
			text := "Compare irrigation formats for the café orchard"
			manifest := requestTestManifest("request-curation", workflowdef.RequestCadenceOnce, "")
			manifest.Trigger = "/curate"
			if surface == "default" {
				manifest.Request.Default = text
			}
			mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"request-curation@1.0.0": manifest})
			curator := session.NewHost(sessions, session.Models{Limits: settings.DefaultSessionLimits()}, tools.NewStubRegistry())
			defer curator.Runner.Curation.Wait(t.Context())
			if surface == "manual title" {
				_, err := curator.Chats.Naming.SetTitle(t.Context(), "sess-1", "Manual irrigation decision")
				testutil.FailErr(t, "set manual title", err)
			}
			admission := &observingStartAdmission{}
			mgr.Starts.Barrier = admission
			calls := 0
			mgr.Requests.OnRequestAccepted = func(ctx context.Context, id, request string) {
				if admission.active {
					t.Fatal("curation reentered the workflow start admission")
				}
				if request != text {
					t.Fatalf("accepted request = %q want %q", request, text)
				}
				calls++
				curator.Runner.Curation.AcceptedWorkflowRequest(ctx, id, request)
			}
			req := api.StartWorkflowRunRequest{WorkflowID: manifest.ID, WorkflowVersion: manifest.Version, OperationID: uuid.NewString(), Request: text}
			start := func() error {
				if surface == "slash" {
					_, handled, err := mgr.Slash.TrySlashPrompt(t.Context(), "sess-1", "/curate "+text, req.OperationID)
					if !handled {
						t.Fatal("declared slash was not handled")
					}
					return err
				}
				if surface == "API start" {
					_, err := mgr.Starts.Start(hostctx.WithHumanWorkflowStart(t.Context()), "sess-1", req)
					return err
				}
				if surface == "default" || surface == "answer" {
					req.Request = ""
				}
				_, err := mgr.Starts.StartHuman(t.Context(), "sess-1", req)
				return err
			}
			testutil.FailErr(t, "start workflow", start())
			if surface == "answer" {
				if calls != 0 {
					t.Fatal("unanswered request was curated")
				}
				run, err := mgr.Store.Runs.ActiveBySession(t.Context(), "sess-1")
				testutil.FailErr(t, "get pending run", err)
				_, err = mgr.Feedback.ResolveUserFeedback(workflowCaller(t, mgr), "sess-1", run.ID, runstate.WorkflowRequestFeedbackID, text)
				testutil.FailErr(t, "answer workflow request", err)
				_, err = mgr.Feedback.ResolveUserFeedback(workflowCaller(t, mgr), "sess-1", run.ID, runstate.WorkflowRequestFeedbackID, text)
				if err == nil {
					t.Fatal("duplicate answer accepted")
				}
			}
			curator.Runner.Curation.Wait(t.Context())
			got, err := sessions.Get(t.Context(), "sess-1")
			testutil.FailErr(t, "get named session", err)
			want := naming.NameSession(t.Context(), nil, text)
			if surface == "manual title" {
				want = "Manual irrigation decision"
			}
			if got.Title != want {
				t.Fatalf("title = %q want %q", got.Title, want)
			}
			testutil.FailErr(t, "replay workflow start", start())
			if calls != 1 {
				t.Fatalf("curation calls after replay = %d want 1", calls)
			}
		})
	}
}

func TestWorkflowCurationExcludesRejectedAndAmbientStarts(t *testing.T) {
	for _, variant := range []string{"no human authority", "unknown workflow", "stopping", "ambient"} {
		t.Run(variant, func(t *testing.T) {
			mgr, _, _, _ := testManager(t)
			manifest := requestTestManifest("curation-rejected", workflowdef.RequestCadenceOnce, "Review the orchard")
			if variant == "ambient" {
				manifest.Attach.Policy = workflowdef.AttachPolicySessionCreate
			}
			mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"curation-rejected@1.0.0": manifest})
			mgr.Requests.OnRequestAccepted = func(context.Context, string, string) { t.Fatal("unaccepted human request was curated") }
			req := api.StartWorkflowRunRequest{WorkflowID: manifest.ID, WorkflowVersion: manifest.Version, Request: "Review the orchard"}
			var err error
			switch variant {
			case "no human authority":
				_, err = mgr.Starts.Start(t.Context(), "sess-1", req)
			case "unknown workflow":
				req.WorkflowID = "unknown-workflow"
				_, err = mgr.Starts.StartHuman(t.Context(), "sess-1", req)
			case "stopping":
				mgr.Starts.Barrier = rejectingSessionAdmission{}
				_, err = mgr.Starts.StartHuman(t.Context(), "sess-1", req)
			case "ambient":
				_, err = mgr.Ambient.StartAmbient(t.Context(), "sess-1", manifest.ID, manifest.Version)
			}
			if variant == "ambient" {
				testutil.FailErr(t, "ambient attach", err)
			} else if err == nil {
				t.Fatal("invalid start accepted")
			}
		})
	}
}

type rejectingSessionAdmission struct{}

func (rejectingSessionAdmission) WithSessionTreeAdmission(context.Context, string, func() error) error {
	return lifecycle.ErrStopping
}
