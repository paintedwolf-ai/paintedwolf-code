package gitcmd

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestPreparedGitPathsFollowSequentialDirectoryFrames(t *testing.T) {
	h := newUninitializedHarness(t)
	for _, name := range []string{"add", "restore", "status"} {
		t.Run(name, func(t *testing.T) {
			prepared := preparePolicyGit(t, h, "git", "-C", "outer", "-C", "repository", name, "--", "nested/file.go")
			repository := filepath.Join(h.root, "outer/repository")
			operand := filepath.Join(repository, "nested/file.go")
			foundOperand, foundMutation := false, false
			for _, target := range prepared.Review.PathTargets {
				if target.Path == operand {
					foundOperand = true
					if target.Write != (name == "restore") {
						t.Fatalf("operand direction changed: %+v", target)
					}
				}
				if target.Path == repository && target.Write {
					foundMutation = true
				}
				if target.Path == filepath.Join(h.root, "outer") && target.Write {
					t.Fatal("directory traversal acquired mutation authority")
				}
			}
			if !foundOperand || foundMutation != (name != "status") {
				t.Fatalf("Git review paths did not follow its addressed repository: %+v", prepared.Review.PathTargets)
			}
		})
	}
}

func TestPreparedGitCloneScopesOnlyLocalSourcePaths(t *testing.T) {
	h := newUninitializedHarness(t)
	for _, test := range []struct {
		source string
		local  string
	}{
		{source: "source", local: filepath.Join(h.root, "outer/source")},
		{source: "file://" + filepath.Join(h.root, "source"), local: filepath.Join(h.root, "source")},
		{source: "https://example.test/repository"},
		{source: "git@example.test:repository"},
	} {
		t.Run(test.source, func(t *testing.T) {
			prepared := preparePolicyGit(t, h, "git", "-C", "outer", "clone", test.source, "destination")
			var sourcePaths []string
			foundDestination := false
			for _, target := range prepared.Review.PathTargets {
				if !target.Write && target.Path != filepath.Join(h.root, "outer") {
					sourcePaths = append(sourcePaths, target.Path)
				}
				if target.Write && target.Path == filepath.Join(h.root, "outer/destination") {
					foundDestination = true
				}
			}
			if !foundDestination || test.local == "" && len(sourcePaths) != 0 || test.local != "" && (len(sourcePaths) != 1 || sourcePaths[0] != test.local) {
				t.Fatalf("clone scopes = %+v", prepared.Review.PathTargets)
			}
		})
	}
}

func preparePolicyGit(t *testing.T, h *harness, words ...string) Prepared {
	t.Helper()
	inv := toolcontract.OperationInvocation{
		Kind:     toolcontract.ProgramChildInvocation,
		Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "node", ExecutionID: "execution"},
		ActorID:  "actor", SessionID: "session", Scope: toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: "project", RootID: "root"},
		SourceUnitID: "program", Span: toolcontract.SourceSpan{Line: 1, Column: 1}, CatalogDigest: toolcontract.LanguageCatalogDigest(),
	}
	var argv commandprogram.Fields
	for _, word := range words {
		argv = append(argv, commandprogram.Field{Value: word, Span: inv.Span})
	}
	request := commandprogram.OperationRequest{Invocation: inv, OperationID: "git", Statement: commandsurface.Statement{SyntaxLocation: commandsurface.SyntaxLocation{ID: "node", Span: inv.Span}}, Argv: argv}
	prepared, err := h.owners.PrepareGit(h.paths, nil, nil, request)
	testutil.FailErr(t, "prepare directory-bound Git form", err)
	return prepared
}
