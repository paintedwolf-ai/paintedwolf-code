package settings

import (
	"path/filepath"
	"testing"

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
	g := &RuleApprovalGate{sources: NoSources()}
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
	if len(targets) != 2 || targets[0].WithinConfinement || !targets[1].WithinConfinement {
		t.Fatalf("mixed native targets = %+v", targets)
	}
	action.Execution.Contained.GrantedWriteRoots = []string{shared}
	targets = g.declaredFileTargets(action)
	if len(targets) != 2 || !targets[0].WithinConfinement {
		t.Fatalf("reviewed write = %+v", targets)
	}
}
