package app

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestBuildRegisteredTurnToolsUseSessionLoading(t *testing.T) {
	testutil.SkipIfShort(t, "assembles the production tool and session graph")
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-turn-sources")
	app, err := Build(t.Context(), testBuildConfig(t, configlayout.FindModuleRoot()))
	testutil.FailErr(t, "build turn sources", err)
	t.Cleanup(func() { _ = app.Close() })
	testdbseed.InsertProjectRoot(t, app.DB, testdbseed.DefaultProjectID, t.TempDir())
	sess, err := app.Sessions.Manager.Chats.CreateForProject(t.Context(), testdbseed.DefaultProjectID, wire.SessionPostureBuild)
	testutil.FailErr(t, "create turn source session", err)
	decider := &decidetest.Fake{}
	app.Sessions.Manager.Coordinator.Loading.SetDecider(decider)

	for _, route := range []struct{ tool, trigger string }{
		{"request_tools", store.TurnLoadTriggerRequest},
		{"skills_read", store.TurnLoadTriggerLookup},
	} {
		t.Run(route.tool, func(t *testing.T) {
			callID := uuid.NewString()
			toolContext := tools.ToolContext{Identity: tools.InvocationIdentity{
				SessionID: sess.ID, ProjectID: sess.ProjectID, Agent: "implement", ToolCallID: callID,
			}}
			_, err := app.ToolRegistry.Run(t.Context(), route.tool, map[string]any{"need": "inspect fixture diagnostics"}, toolContext)
			if err != nil && toolrejection.AsToolReject(err) == nil {
				testutil.FailErr(t, "run registered turn tool", err)
			}
			receipt, found, err := app.Sessions.Store.LatestTurnLoadReceipt(t.Context(), sess.ID)
			testutil.FailErr(t, "read durable turn receipt", err)
			if !found || receipt.Trigger != route.trigger || receipt.ToolCallID != callID {
				t.Fatalf("registered %s bypassed session loading: receipt=%+v, found=%v", route.tool, receipt, found)
			}
			if route.tool == "request_tools" && (len(decider.Ranks) == 0 || receipt.Engine == "") {
				t.Fatal("registered request_tools bypassed the session decision engine")
			}
		})
	}
}
