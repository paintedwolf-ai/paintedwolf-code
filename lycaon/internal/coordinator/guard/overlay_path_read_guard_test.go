package guard_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

const testSandboxReadPath = "/Users/me/.config/paintedwolf/worker-branches/deadbeef/job-a/shellsim/builtins.py"

func TestObserveCoordinatorWorkerBranchPathBlocksSandboxPath(t *testing.T) {
	sess := &api.Session{ID: "parent-1", AgentType: "coordinator"}
	gc := oar.NewGuardContext()
	guard.ObserveCoordinatorWorkerBranchPath(
		sess,
		"read",
		map[string]any{"path": testSandboxReadPath},
		gc)

	if !observeHasCode(gc, guard.CoordinatorOverlayPathReadForbiddenCode) {
		t.Fatalf("want %s in %v", guard.CoordinatorOverlayPathReadForbiddenCode, gc.ArgValidationErrors)
	}
	if gc.RejectData[guard.CoordinatorOverlayPathReadForbiddenCode]["path"] != "shellsim/builtins.py" {
		t.Fatalf("reject path = %#v want repo-relative", gc.RejectData[guard.CoordinatorOverlayPathReadForbiddenCode])
	}
}

func TestObserveCoordinatorWorkerBranchPathBlocksCommandCwd(t *testing.T) {
	sess := &api.Session{ID: "parent-1", AgentType: "coordinator"}
	gc := oar.NewGuardContext()
	guard.ObserveCoordinatorWorkerBranchPath(
		sess,
		"command",
		map[string]any{"cwd": "/Users/me/.config/paintedwolf/worker-branches/deadbeef/job-a"},
		gc)
	if !observeHasCode(gc, guard.CoordinatorOverlayPathReadForbiddenCode) {
		t.Fatalf("want %s for command cwd", guard.CoordinatorOverlayPathReadForbiddenCode)
	}
	if gc.RejectData[guard.CoordinatorOverlayPathReadForbiddenCode]["path"] != "." {
		t.Fatalf("reject path = %#v want '.'", gc.RejectData[guard.CoordinatorOverlayPathReadForbiddenCode])
	}
}

func TestObserveCoordinatorWorkerBranchPathBlocksReplaceLines(t *testing.T) {
	sess := &api.Session{ID: "parent-1", AgentType: "coordinator"}
	gc := oar.NewGuardContext()
	guard.ObserveCoordinatorWorkerBranchPath(
		sess,
		"replace_lines",
		map[string]any{"path": testSandboxReadPath},
		gc)
	if !observeHasCode(gc, guard.CoordinatorOverlayPathReadForbiddenCode) {
		t.Fatalf("want %s for replace_lines", guard.CoordinatorOverlayPathReadForbiddenCode)
	}
}

func TestObserveCoordinatorWorkerBranchPathAllowsPrimaryPath(t *testing.T) {
	sess := &api.Session{ID: "parent-1", AgentType: "coordinator"}
	gc := oar.NewGuardContext()
	guard.ObserveCoordinatorWorkerBranchPath(
		sess,
		"read",
		map[string]any{"path": "shellsim/builtins.py"},
		gc)

	if observeHasCode(gc, guard.CoordinatorOverlayPathReadForbiddenCode) {
		t.Fatal("primary read should pass")
	}
}

func TestObserveCoordinatorWorkerBranchPathSkipsWorkerChild(t *testing.T) {
	sess := &api.Session{ID: "child-1", ParentSessionID: "parent-1", AgentType: "implementer"}
	gc := oar.NewGuardContext()
	guard.ObserveCoordinatorWorkerBranchPath(
		sess,
		"read",
		map[string]any{"path": testSandboxReadPath},
		gc)

	if observeHasCode(gc, guard.CoordinatorOverlayPathReadForbiddenCode) {
		t.Fatal("worker child should pass")
	}
}

func TestObserveCoordinatorWorkerBranchPathChecksEveryCandidate(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"cwd after primary path": {"path": "README.md", "cwd": testSandboxReadPath},
		"string paths":           {"paths": []string{"README.md", testSandboxReadPath}},
		"decoded paths":          {"paths": []any{"README.md", 17, testSandboxReadPath}},
	} {
		t.Run(name, func(t *testing.T) {
			gc := oar.NewGuardContext()
			guard.ObserveCoordinatorWorkerBranchPath(&api.Session{ID: "parent", AgentType: "coordinator"}, "read", args, gc)
			if !gc.PathIsWorkerBranch || gc.RejectData[guard.CoordinatorOverlayPathReadForbiddenCode]["path"] != "shellsim/builtins.py" {
				t.Fatalf("worker path hidden by earlier primary path: %+v", gc.RejectData)
			}
		})
	}
}
