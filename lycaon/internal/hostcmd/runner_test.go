package hostcmd

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/exec"
)

// validateLine parses a command line and validates its stages, as dispatch does.
func validateLine(r *Runner, line string) error {
	stages, err := exec.StagesFromCommandLine(line)
	if err != nil {
		return err
	}
	return r.ValidateStages(context.Background(), stages)
}

func TestValidateStagesOpenWhenNoPatterns(t *testing.T) {
	r := NewRunner()

	for _, cmd := range []string{
		"go test ./...",
		"pnpm test",
		"cargo check",
		"pytest",
		"python --version",
		"mix test",
		"dotnet test",
	} {
		if err := validateLine(r, cmd); err != nil {
			t.Fatalf("allowed %q: %v", cmd, err)
		}
	}

	// Sequences, pipelines, and file redirections parse into distinct stages.
	for cmd, stages := range map[string]int{
		"go test; rm -rf /tmp/x": 2,
		"go test && make":        2,
		"which go || echo none":  2,
		"pytest | tee out.log":   2,
		"go test > out.log":      1,
	} {
		parsed, err := exec.StagesFromCommandLine(cmd)
		if err != nil {
			t.Fatalf("parse %q: %v", cmd, err)
		}
		if len(parsed) != stages {
			t.Fatalf("%q parsed into %d stages, want %d", cmd, len(parsed), stages)
		}
		if err := r.ValidateStages(context.Background(), parsed); err != nil {
			t.Fatalf("sequenced %q: %v", cmd, err)
		}
	}

	// Shell substitutions, incomplete redirections, and bare & reject.
	for cmd, want := range map[string]error{
		"go build $(git rev-parse HEAD)": argv.ErrShellMetacharacters,
		"go test `id -u`":                argv.ErrShellMetacharacters,
		"go test >":                      argv.ErrRedirectionTargetRequired,
		"python3 -m http.server &":       argv.ErrBackgroundOperator,
	} {
		if err := validateLine(r, cmd); !errors.Is(err, want) {
			t.Fatalf("%q = %v, want %v", cmd, err, want)
		}
	}
}

func TestValidateStagesRejectsMetacharInStage(t *testing.T) {
	if err := validateLine(NewRunner(), "echo $(bad)"); !errors.Is(err, argv.ErrShellMetacharacters) {
		t.Fatalf("err = %v, want %v", err, argv.ErrShellMetacharacters)
	}
}

func TestValidateStagesAcceptsArgsWithEmbeddedQuotes(t *testing.T) {
	r := NewRunner()
	ctx := context.Background()
	// Quotes inside parsed argv are literal data.
	stages := []exec.Stage{
		{Name: "grep", Args: []string{`say "yes`, "file"}},
		{Name: "python3", Args: []string{"-c", `print("hi")`}},
	}
	if err := r.ValidateStages(ctx, stages); err != nil {
		t.Fatalf("valid argv rejected: %v", err)
	}
}

func TestValidateStagesAcceptsOddQuoteArg(t *testing.T) {
	if err := validateLine(NewRunner(), `grep 'say "yes' file`); err != nil {
		t.Fatalf("single-quoted arg with embedded double quote rejected: %v", err)
	}
}
