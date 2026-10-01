package commandsurface_test

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/exec"
)

func TestParsePlanCommand(t *testing.T) {
	stages, err := stagesOf(map[string]any{"command": "go version"})
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if len(stages) != 1 || stages[0].Name != "go" {
		t.Fatalf("stages = %#v", stages)
	}
}

func TestParsePlanPipeline(t *testing.T) {
	stages, err := stagesOf(map[string]any{
		"pipeline": []any{"echo hello", "wc -c"},
	})
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if len(stages) != 2 {
		t.Fatalf("stages = %d, want 2", len(stages))
	}
}

func TestParsePlanMutuallyExclusive(t *testing.T) {
	_, err := stagesOf(map[string]any{
		"command":  "go version",
		"pipeline": []any{"echo hi"},
	})
	if !errors.Is(err, commandsurface.ErrArgvConflict) {
		t.Fatalf("err = %v want ErrArgvConflict", err)
	}
}

func TestParsePlanMissing(t *testing.T) {
	_, err := stagesOf(map[string]any{"cwd": ".", "timeout_ms": 120000})
	if !errors.Is(err, commandsurface.ErrArgvRequired) {
		t.Fatalf("err = %v want ErrArgvRequired", err)
	}
}

func TestParsePlanWhitespaceCommand(t *testing.T) {
	_, err := stagesOf(map[string]any{"command": "   "})
	if !errors.Is(err, commandsurface.ErrArgvRequired) {
		t.Fatalf("err = %v want ErrArgvRequired", err)
	}
}

func TestParsePlanEmptyPipeline(t *testing.T) {
	_, err := stagesOf(map[string]any{"pipeline": []any{}})
	if !errors.Is(err, commandsurface.ErrArgvRequired) {
		t.Fatalf("err = %v want ErrArgvRequired", err)
	}
}

func TestParsePlanPipelineShape(t *testing.T) {
	_, err := stagesOf(map[string]any{"pipeline": "echo hi"})
	if !errors.Is(err, commandsurface.ErrPipelineShape) {
		t.Fatalf("err = %v want ErrPipelineShape", err)
	}
}

func TestParsePlanPipelineEmptyElement(t *testing.T) {
	_, err := stagesOf(map[string]any{"pipeline": []any{"echo hi", "  "}})
	if !errors.Is(err, commandsurface.ErrPipelineShape) {
		t.Fatalf("err = %v want ErrPipelineShape", err)
	}
}

func TestPrimaryCommandLinePipeline(t *testing.T) {
	line := commandsurface.PrimaryCommandLine(map[string]any{
		"pipeline": []any{"git log --oneline", "head -20"},
	}, nil)
	if line != "git log --oneline | head -20" {
		t.Fatalf("line = %q", line)
	}
}

func TestParsePlanExportFolding(t *testing.T) {
	stages, err := stagesOf(map[string]any{
		"command": "export FOO=bar && go test ./...",
	})
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if len(stages) != 1 {
		t.Fatalf("len(stages) = %d, want 1", len(stages))
	}
	if stages[0].Name != "go" || stages[0].Env["FOO"] != "bar" {
		t.Fatalf("stages[0] = %#v", stages[0])
	}
}

func TestParsePlanExportAloneRejects(t *testing.T) {
	_, err := stagesOf(map[string]any{
		"command": "export FOO=bar",
	})
	if err == nil {
		t.Fatal("expected export alone to reject")
	}
}

func TestJoinCwdVirtualRootsAndRelativeAtPaths(t *testing.T) {
	// Virtual root @scratch is not prefixed with cwd.
	planScratch, err := commandsurface.ParsePlan(map[string]any{
		"command": "echo hi > @scratch/out.txt",
		"cwd":     "sub",
	})
	if err != nil {
		t.Fatalf("ParsePlan @scratch: %v", err)
	}
	writes := planScratch.InlineWrites()
	if len(writes) != 1 || writes[0].Path != "@scratch/out.txt" {
		t.Fatalf("writes[0].Path = %q, want @scratch/out.txt", writes[0].Path)
	}

	// Non-virtual @-prefixed path is joined under cwd.
	planTypes, err := commandsurface.ParsePlan(map[string]any{
		"command": "echo hi > @types/node/index.d.ts",
		"cwd":     "sub",
	})
	if err != nil {
		t.Fatalf("ParsePlan @types: %v", err)
	}
	writesTypes := planTypes.InlineWrites()
	if len(writesTypes) != 1 || writesTypes[0].Path != "sub/@types/node/index.d.ts" {
		t.Fatalf("writesTypes[0].Path = %q, want sub/@types/node/index.d.ts", writesTypes[0].Path)
	}
}

func stagesOf(args map[string]any) ([]exec.Stage, error) {
	plan, err := commandsurface.ParsePlan(args)
	return plan.Stages, err
}
