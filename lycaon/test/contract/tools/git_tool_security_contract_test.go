package contract

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/toolrejection"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestGitRestoreOutsideScopeSecurity(t *testing.T) {
	t.Parallel()
	b := contractcheck.ProdToolBoundary(t)
	_, err := native.GitMutations{Git: git.NewManager(), Boundary: b}.Restore(context.Background(), tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	}, git.GitRestoreOpts{Paths: []string{".git/HEAD"}})
	if err == nil {
		t.Fatal("expected restore path denial")
	}
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "GIT_INTERNALS_WRITE_DENIED" {
		t.Fatalf("err = %v want GIT_INTERNALS_WRITE_DENIED", err)
	}
}

func TestGitCommitOutsideScopeSecurity(t *testing.T) {
	t.Parallel()
	b := contractcheck.ProdToolBoundary(t)
	_, _, err := native.GitMutations{Git: git.NewManager(), Boundary: b}.Commit(context.Background(), tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	}, native.GitCommitRequest{Message: "nope", Paths: []string{".git/HEAD"}})
	if err == nil {
		t.Fatal("expected commit path denial")
	}
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "GIT_INTERNALS_WRITE_DENIED" {
		t.Fatalf("err = %v want GIT_INTERNALS_WRITE_DENIED", err)
	}
}
