package contract

import (
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestWorkerAssignmentForwardsAttachments(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)

	dto := contractcheck.ReadRepoFile(t, root, "lycaon/internal/coordinator/inject/worker_task_assignment_inject.go")
	if !strings.Contains(dto, "Attachments") || !strings.Contains(dto, "promptattach.ForwardedAttachment") {
		t.Fatal("WorkerTaskAssignment DTO must carry forwarded attachments")
	}

	compose := contractcheck.ReadRepoFile(t, root, "lycaon/internal/app/build_tools.go")
	if !strings.Contains(compose, "SessionForwardedAttachments") {
		t.Fatal("task() ComposePrompt must collect parent attachments")
	}

	tmpl := contractcheck.ReadRepoFile(t, root, "lycaon/config/packs/painted-wolf/security/guidance/worker-task-assignment.md")
	if !strings.Contains(tmpl, "{% if attachments %}") || !strings.Contains(tmpl, "{{ a.path }}") {
		t.Fatal("worker-task-assignment inject must render attachments")
	}
	if !strings.Contains(tmpl, "metadata only") {
		t.Fatal("assignment template must identify attachment metadata")
	}
}
