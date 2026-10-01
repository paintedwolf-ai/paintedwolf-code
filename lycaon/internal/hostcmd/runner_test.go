package hostcmd

import (
	"context"
	"testing"

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
	for _, cmd := range []string{
		"go test; rm -rf /tmp/x",
		"go test && make",
		"which go || echo none",
		"pytest | tee out.log",
		"go test > out.log",
	} {
		if err := validateLine(r, cmd); err != nil {
			t.Fatalf("sequenced %q: %v", cmd, err)
		}
	}

	// Shell substitutions, incomplete redirections, and bare & reject.
	for _, cmd := range []string{
		"go build $(git rev-parse HEAD)",
		"echo `whoami`",
		"go test >",
		"python3 -m http.server &",
	} {
		if err := validateLine(r, cmd); err == nil {
			t.Fatalf("expected block for %q", cmd)
		}
	}
}

func TestValidateStagesRejectsMetacharInStage(t *testing.T) {
	if err := validateLine(NewRunner(), "echo $(bad)"); err == nil {
		t.Fatal("expected metachar rejection")
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
