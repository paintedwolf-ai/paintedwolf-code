package tools_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

type validatingBranchWorkspace struct {
	err error
}

func (b validatingBranchWorkspace) ValidateMeta(context.Context) error        { return b.err }
func (validatingBranchWorkspace) EnsureParents(context.Context, string) error { return nil }

func TestValidateWorkerBranchChecksMetadata(t *testing.T) {
	want := errors.New("invalid branch metadata")
	err := tools.ValidateWorkerBranch(context.Background(), tools.ToolContext{
		Source: tools.InvocationSource{WorkerBranchRoot: t.TempDir(),
			BranchWorkspace: validatingBranchWorkspace{err: want}},
	})
	if !errors.Is(err, want) {
		t.Fatalf("ValidateWorkerBranch error = %v, want %v", err, want)
	}
}

func TestValidateWorkerBranchAllowsCanonicalWorkspace(t *testing.T) {
	testutil.FailErr(t, "ValidateWorkerBranch", tools.ValidateWorkerBranch(context.Background(), tools.ToolContext{}))
}
