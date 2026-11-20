package wiring

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// The assembled host lists the coordinator's tools by session posture and
// active run; file tools stay off the coordinator surface in every row.
func TestFrozenPostureToolMatrix(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	for _, row := range []struct {
		name    string
		posture api.SessionPosture
		withRun bool
		include []string
		exclude []string
	}{
		{
			name:    "spec",
			posture: api.SessionPostureSpec,
			include: []string{"restore_version", "workflow_compose", "command"},
			exclude: []string{"plan_from_blueprint"},
		},
		{
			name:    "build",
			posture: api.SessionPostureBuild,
			include: []string{"plan_from_blueprint", "task", "state_start", "workflow_advance", "workflow_transition", "command"},
		},
		{
			name:    "build+active_run",
			posture: api.SessionPostureBuild,
			withRun: true,
			include: []string{"restore_version", "task", "state_start", "workflow_advance", "workflow_transition", "command"},
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			// Every row's folder joins the same project; the first becomes its
			// primary root, so each must outlive its own subtest.
			sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: row.posture}, h.ProjectDir(t, "posture-"+row.name))
			testutil.FailErr(t, "create session", err)
			if row.withRun {
				_, err := h.Workflows.Manager.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{WorkflowID: "plan", WorkflowVersion: "1.0.1"})
				testutil.FailErr(t, "start plan run", err)
			}
			var listed []string
			for _, meta := range h.Sessions.Manager.Coordinator.Guards.Policy().ListForPrompt(ctx, sess, "coordinator") {
				listed = append(listed, meta.Name)
			}
			for _, tool := range row.include {
				if !slices.Contains(listed, tool) {
					t.Errorf("%q missing from listed tools: %v", tool, listed)
				}
			}
			for _, tool := range append(row.exclude, "read", "write", "edit", "grep", "git_diff") {
				if slices.Contains(listed, tool) {
					t.Errorf("%q listed: %v", tool, listed)
				}
			}
		})
	}
}
