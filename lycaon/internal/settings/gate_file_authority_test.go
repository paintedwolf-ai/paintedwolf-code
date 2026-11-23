package settings

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
)

func TestNativeWriteTargetsRequireMutationAuthority(t *testing.T) {
	project, shared := t.TempDir(), t.TempDir()
	t.Setenv("TMPDIR", shared)
	path := filepath.Join(shared, "note.txt")
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{Files: []string{path}},
		Scope:      hitl.ActionScope{ProjectDir: project},
		Execution:  hitl.ActionExecution{Contained: hitl.Contained{FSJailed: true, Roots: []string{project}, WriteRoots: []string{project, shared}}},
	}
	g := &RuleApprovalGate{sources: emptySources()}
	for _, name := range pathMutatingTools {
		action.Invocation.Tool = name
		targets := g.declaredFileTargets(action)
		if len(targets) != 1 || !targets[0].OutsideRoots || targets[0].WithinConfinement {
			t.Errorf("%s targets = %+v", name, targets)
		}
	}
	action.Invocation.NativePathTargets = []hitl.NativePathTarget{{Path: path, Write: true}, {Path: path}}
	action.Invocation.Tool = "command"
	targets := g.declaredFileTargets(action)
	if len(targets) != 2 || targets[0].WithinConfinement || targets[1].WithinConfinement {
		t.Fatalf("mixed native targets = %+v", targets)
	}
	action.Scope.GrantedWriteRoots = []string{shared}
	targets = g.declaredFileTargets(action)
	if len(targets) != 2 || !targets[0].WithinConfinement || targets[1].WithinConfinement {
		t.Fatalf("write-only grant = %+v", targets)
	}
	action.Scope.SessionReadPaths = []string{shared}
	targets = g.declaredFileTargets(action)
	if len(targets) != 2 || !targets[0].WithinConfinement || !targets[1].WithinConfinement {
		t.Fatalf("reviewed read and write = %+v", targets)
	}

	// Copy and extract_archive evaluate source reads and destination writes independently.
	srcFile := filepath.Join(shared, "incoming.zip")
	dstDir := filepath.Join(project, "extracted")
	copyAction := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "copy",
			Args: map[string]any{"copies": []any{map[string]any{"from": srcFile, "to": dstDir}}},
		},
		Scope:     hitl.ActionScope{ProjectDir: project, SessionReadPaths: []string{shared}},
		Execution: hitl.ActionExecution{Contained: hitl.Contained{FSJailed: true, Roots: []string{project}, WriteRoots: []string{project}}},
	}
	copyTargets := g.declaredFileTargets(copyAction)
	if len(copyTargets) != 1 || copyTargets[0].Mode != gate.ModeRead || !copyTargets[0].WithinConfinement {
		t.Fatalf("copy from granted read-only temp into project = %+v", copyTargets)
	}
	extractAction := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "extract_archive",
			Args: map[string]any{"path": srcFile, "dest": dstDir},
		},
		Scope:     hitl.ActionScope{ProjectDir: project, SessionReadPaths: []string{shared}},
		Execution: hitl.ActionExecution{Contained: hitl.Contained{FSJailed: true, Roots: []string{project}, WriteRoots: []string{project}}},
	}
	extractTargets := g.declaredFileTargets(extractAction)
	if len(extractTargets) != 1 || extractTargets[0].Mode != gate.ModeRead || !extractTargets[0].WithinConfinement {
		t.Fatalf("extract_archive from granted read-only temp into project = %+v", extractTargets)
	}
}
