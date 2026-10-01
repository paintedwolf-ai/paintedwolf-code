package commandsurface

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/exec"
)

func TestParsePlanAcceptsSequence(t *testing.T) {
	stages, err := stagesOf(map[string]any{"command": "go build ./... && ./app"})
	if err != nil {
		t.Fatalf("sequenced command rejected: %v", err)
	}
	if len(stages) != 2 {
		t.Fatalf("stages = %d, want 2", len(stages))
	}
}

// Child-process builtins cannot affect later sequence stages.
func TestParsePlanRejectsShellBuiltins(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    error
	}{
		{"cd pkg", ErrDirectoryChangeCommand},
		{"cd pkg && go test ./...", ErrDirectoryChangeCommand},
		{"mkdir -p pkg && cd pkg && go mod init x", ErrDirectoryChangeCommand},
		{"export NODE_ENV=test", argv.ErrEnvAssignmentCommand},
		{"go build && export PATH=/x", argv.ErrEnvAssignmentCommand},
		{"NODE_ENV=test", argv.ErrEnvAssignmentCommand},
		{"go build && TOKEN=value", argv.ErrEnvAssignmentCommand},
	} {
		_, err := stagesOf(map[string]any{"command": tc.command})
		if !errors.Is(err, tc.want) {
			t.Fatalf("stagesOf(%q) error = %v, want %v", tc.command, err, tc.want)
		}
	}
}

func TestParsePlanAcceptsInlineEnvironment(t *testing.T) {
	stages, err := stagesOf(map[string]any{"command": "NODE_ENV=test go build"})
	if err != nil {
		t.Fatalf("ParsePlan error = %v", err)
	}
	if len(stages) != 1 {
		t.Fatalf("len(stages) = %d, want 1", len(stages))
	}
	if stages[0].Name != "go" || len(stages[0].Args) != 1 || stages[0].Args[0] != "build" {
		t.Errorf("stages[0] = %q %v, want go [build]", stages[0].Name, stages[0].Args)
	}
	if stages[0].Env["NODE_ENV"] != "test" {
		t.Errorf("stages[0].Env = %v, want NODE_ENV=test", stages[0].Env)
	}

	stages, err = stagesOf(map[string]any{"command": "go build && TOKEN=value ./app"})
	if err != nil {
		t.Fatalf("ParsePlan sequence error = %v", err)
	}
	if len(stages) != 2 {
		t.Fatalf("len(stages) = %d, want 2", len(stages))
	}
	if len(stages[0].Env) != 0 {
		t.Errorf("stages[0].Env = %v, want empty", stages[0].Env)
	}
	if stages[1].Name != "./app" || stages[1].Env["TOKEN"] != "value" {
		t.Errorf("stages[1] = %q env=%v, want ./app with TOKEN=value", stages[1].Name, stages[1].Env)
	}

	// Blocked key rejection:
	_, err = stagesOf(map[string]any{"command": "LD_PRELOAD=/evil ./app"})
	if !errors.Is(err, exec.ErrBlockedEnvKey) {
		t.Fatalf("blocked env key err = %v, want ErrBlockedEnvKey", err)
	}
}

// A sequence has no unambiguous stdin target.
func TestParsePlanRejectsStdinOnSequence(t *testing.T) {
	for _, args := range []map[string]any{
		{"command": "cat && wc -l", "stdin": "hello"},
		{"command": "cat; wc -l", "stdin_from": "in.txt"},
	} {
		if _, err := stagesOf(args); !errors.Is(err, ErrStdinWithSequence) {
			t.Fatalf("stagesOf(%v) error = %v, want %v", args, err, ErrStdinWithSequence)
		}
	}

	// A pipeline still feeds its head stage.
	if _, err := stagesOf(map[string]any{
		"pipeline": []string{"grep x", "wc -l"}, "stdin": "hello",
	}); err != nil {
		t.Fatalf("pipeline with stdin rejected: %v", err)
	}
}

// PlanGroups provides the matcher's structured execution view.
func TestPlanGroupsGroupsPrograms(t *testing.T) {
	plan, ok := PlanGroups(map[string]any{"command": "go build ./... && ./app -v"})
	if !ok {
		t.Fatal("PlanGroups reported no plan for a valid sequence")
	}
	if len(plan) != 2 {
		t.Fatalf("groups = %d, want 2 (%v)", len(plan), plan)
	}
	if plan[0][0] != "go build ./..." || plan[1][0] != "./app -v" {
		t.Fatalf("plan = %v", plan)
	}

	// A pipeline is one group holding its stages.
	plan, ok = PlanGroups(map[string]any{"pipeline": []string{"git log", "head -20"}})
	if !ok || len(plan) != 1 || len(plan[0]) != 2 {
		t.Fatalf("pipeline plan = %v, ok = %v", plan, ok)
	}

	// Quoted operators remain argument data.
	plan, ok = PlanGroups(map[string]any{"command": `echo "a \" && rm -rf /"`})
	if !ok || len(plan) != 1 || len(plan[0]) != 1 {
		t.Fatalf("quoted-operator plan = %v, ok = %v", plan, ok)
	}

	if _, ok := PlanGroups(map[string]any{"command": "echo $(whoami)"}); ok {
		t.Fatal("a line the grammar rejects must report no plan")
	}
}

func stagesOf(args map[string]any) ([]exec.Stage, error) {
	plan, err := ParsePlan(args)
	return plan.Stages, err
}
