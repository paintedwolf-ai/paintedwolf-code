package toolusage

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGenerateRequiresExactWorkflowVersion(t *testing.T) {
	fake := &generateSidecar{t: t, runVersion: "9.0.0"}
	server := httptest.NewServer(fake)
	defer server.Close()
	client := newLiveClient(server.URL, "fixture")
	if err := client.enableWorkflowFixture(t.Context(), "project", "implement-dispatch", "1.0.0"); err == nil {
		t.Fatal("catalog version mismatch accepted")
	}
	if err := client.enableWorkflowFixture(t.Context(), "project", "implement-dispatch", "2.3.4"); err != nil {
		t.Fatalf("exact catalog workflow: %v", err)
	}
	err := client.startWorkflowRequest(t.Context(), "s1", GenerateTask{Workflow: "implement-dispatch", WorkflowVersion: "2.3.4", CorpusTask: CorpusTask{Prompt: "inspect"}}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "9.0.0") {
		t.Fatalf("response identity mismatch error=%v", err)
	}
}

func TestGenerateValidatesWorkflowIdentityBeforeCreatingProject(t *testing.T) {
	var manifest bytes.Buffer
	err := RunGenerate(t.Context(), GenerateOptions{
		Tasks: []GenerateTask{{Workflow: "implement-dispatch"}}, Timeout: time.Second, Manifest: &manifest,
		Unattended: UnattendedPolicy{ApprovalGuidance: "no", Answer: "yes", MaxInterventions: 1},
	})
	if err == nil || !strings.Contains(err.Error(), "workflow_version") {
		t.Fatalf("workflow identity error=%v", err)
	}
}
