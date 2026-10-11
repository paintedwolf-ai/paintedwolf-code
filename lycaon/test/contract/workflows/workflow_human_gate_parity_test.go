package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

type humanGateSpec struct {
	name            string
	operationID     string
	path            string
	stubPath        string
	kickConstant    string
	coordinatorTool string
}

func TestHumanGateSatisfyPathParity(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	specs := []humanGateSpec{
		{
			name:            "feedback",
			operationID:     "resolveWorkflowFeedback",
			path:            "/v1/workflow-runs/{id}/feedback/{phase_id}",
			stubPath:        `POST /v1/workflow-runs/{id}/feedback/{phase_id}`,
			kickConstant:    "FeedbackPending",
			coordinatorTool: "workflow_user_feedback",
		},
		{
			name:        "decision",
			operationID: "resolveWorkflowDecision",
			path:        "/v1/workflow-runs/{id}/decisions/{phase_id}",
			stubPath:    `POST /v1/workflow-runs/{id}/decisions/{phase_id}`,
		},
	}

	openAPIRoutes, err := wirespec.LoadOpenAPIRoutes(root)
	contractcheck.FailErr(t, "load OpenAPI routes from docs/openapi.yaml", err)
	openAPISet := wirespec.RouteSet(openAPIRoutes)
	serverRoutes, err := wirespec.LoadServerRoutes(root)
	contractcheck.FailErr(t, "load server routes from internal/api/server.go", err)
	serverSet := wirespec.RouteSet(serverRoutes)
	stubBody, err := os.ReadFile(filepath.Join(root, "lycaon", "test", "contract", "wire", "stub_routes_workflows.go"))
	contractcheck.FailErr(t, "read file", err)
	stubText := string(stubBody)
	feedbackBinding := contractcheck.ReadRepoFile(t, root, "lycaon/internal/app/delegations/context.go")
	feedbackHooks := contractcheck.ReadRepoFile(t, root, "lycaon/internal/app/delegations/hooks.go")
	profileData, err := os.ReadFile(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "profiles", "coordinator.yaml"))
	contractcheck.FailErr(t, "read file", err)
	profileText := string(profileData)
	clientMethods, err := wirespec.ParseTSClientMethods(filepath.Join(root, "lycaon-den", "src", "api", "client.ts"))
	contractcheck.FailErr(t, "parse the Den client methods", err)

	for _, spec := range specs {
		t.Run(spec.name, func(t *testing.T) {
			t.Parallel()
			if _, ok := clientMethods[spec.operationID]; !ok {
				t.Fatalf("Den client has no method %q", spec.operationID)
			}
			key := wirespec.RouteKey("POST", spec.path)
			if _, ok := openAPISet[key]; !ok {
				t.Fatalf("openapi missing POST %s", spec.path)
			}
			if _, ok := serverSet[key]; !ok {
				t.Fatalf("server.go missing POST %s", spec.path)
			}
			if !strings.Contains(stubText, spec.stubPath) {
				t.Fatalf("workflow fixture missing %q", spec.stubPath)
			}
			if spec.kickConstant != "" {
				if !strings.Contains(feedbackBinding, "deps.Workflows.Manager.Feedback.OnFeedbackPending = r.OnWorkflowFeedbackPending") {
					t.Fatal("workflow feedback must bind its pending callback to the delegation runtime")
				}
				if !strings.Contains(feedbackHooks, "Coordinator.Guidance.Emit(ctx, sessionID, anchor."+spec.kickConstant+", anchor.Envelope{})") {
					t.Fatalf("delegation runtime missing %q guidance emission", spec.kickConstant)
				}
			}
			if spec.coordinatorTool != "" && !strings.Contains(profileText, spec.coordinatorTool+": sticky") {
				t.Fatalf("coordinator profile missing sticky %q", spec.coordinatorTool)
			}
		})
	}
}
