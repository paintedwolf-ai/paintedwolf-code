package security

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestNoFolderSessionCreateAndAutoName(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{Pattern: ".*", Text: "I'll help with that."},
	}})
	h := wiring.BuildForTest(t, wiring.WithLLMClient(mock))
	httpSrv := httptest.NewServer(h.Server)
	t.Cleanup(httpSrv.Close)
	base := httpSrv.URL

	project := createAPIProject(t, base, `{"draft":true}`)
	if len(project.Roots) != 1 {
		t.Fatalf("roots = %d want 1 scratch root for draft project", len(project.Roots))
	}
	scratchRoot := project.Roots[0]

	hubCtx, hubCancel := context.WithCancel(t.Context())
	defer hubCancel()
	hubEvents := subscribeProjectEvents(t, hubCtx, base, project.ID)

	sess := openAPIPostJSON[wire.Session](t, base, "/v1/sessions", nil,
		`{"project_id":"`+project.ID+`","posture":"build"}`, http.StatusAccepted)
	if sess.WorkspacePath != scratchRoot.Path {
		t.Fatalf("session workspace_path = %q want the draft scratch root %q", sess.WorkspacePath, scratchRoot.Path)
	}

	userPrompt := "Design a markdown note-taking app with tags"
	acceptPromptOpenAPI(t, base, sess.ID, `{"text":"`+userPrompt+`"}`)

	waitForNamingEvents(t, hubEvents, project.ID, sess.ID)

	named := openAPIGetJSON[wire.Project](t, base, "/v1/projects/{id}",
		map[string]string{"id": project.ID}, http.StatusOK)
	if named.Name == nil || strings.TrimSpace(*named.Name) == "" {
		t.Fatal("expected draft project name from first user prompt")
	}

	updatedSession := openAPIGetJSON[wire.Session](t, base, "/v1/sessions/{id}",
		map[string]string{"id": sess.ID}, http.StatusOK)
	if strings.TrimSpace(updatedSession.Title) == "" {
		t.Fatal("expected session title from first user prompt")
	}

}

func waitForNamingEvents(t *testing.T, events <-chan wire.EventEnvelope, projectID, sessionID string) {
	t.Helper()
	timer := time.NewTimer(testutil.Timeout(3 * time.Second))
	defer timer.Stop()
	var projectNamed, sessionNamed bool
	for !projectNamed || !sessionNamed {
		select {
		case envelope, ok := <-events:
			if !ok {
				t.Fatal("event stream closed before naming completed")
			}
			switch envelope.Topic {
			case wire.EventTopicProject:
				var event wire.ProjectEvent
				testutil.FailErr(t, "decode project naming event", json.Unmarshal(envelope.Data, &event))
				if event.ID == projectID && event.Action == wire.ProjectEventUpdated && event.Project != nil && event.Project.Name != nil {
					projectNamed = projectNamed || strings.TrimSpace(*event.Project.Name) != ""
				}
			case wire.EventTopicSession:
				var event wire.SessionEvent
				testutil.FailErr(t, "decode session naming event", json.Unmarshal(envelope.Data, &event))
				if event.ID == sessionID {
					sessionNamed = sessionNamed || strings.TrimSpace(event.Title) != ""
				}
			default:
			}
		case <-timer.C:
			t.Fatalf("naming events incomplete: project=%t session=%t", projectNamed, sessionNamed)
		}
	}
}
