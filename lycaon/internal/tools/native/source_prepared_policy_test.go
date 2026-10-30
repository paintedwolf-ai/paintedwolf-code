package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestPreparedMutationClassesRefuseStalePolicyWithoutEffects(t *testing.T) {
	operations := map[string]func(context.Context, tools.ToolContext, string) error{
		"metadata": func(ctx context.Context, tc tools.ToolContext, root string) error {
			return chmodAgentPath(ctx, tc, testMutationTarget(filepath.Join(root, "source.txt")), 0o600)
		},
		"rename": func(ctx context.Context, tc tools.ToolContext, root string) error {
			return renameAgentPath(ctx, tc, testMutationTarget(filepath.Join(root, "source.txt")), testMutationTarget(filepath.Join(root, "destination")), nil)
		},
		"directory create": func(ctx context.Context, tc tools.ToolContext, root string) error {
			_, err := mkdirAgentPath(ctx, tc, testMutationTarget(filepath.Join(root, "destination")), 0o755)
			return err
		},
		"file remove": func(ctx context.Context, tc tools.ToolContext, root string) error {
			return removeAgentPath(ctx, tc, testMutationTarget(filepath.Join(root, "source.txt")), nil)
		},
		"link remove": func(ctx context.Context, tc tools.ToolContext, root string) error {
			return removeAgentPath(ctx, tc, testMutationTarget(filepath.Join(root, "alias")), nil)
		},
		"directory remove": func(ctx context.Context, tc tools.ToolContext, root string) error {
			return removeAgentTree(ctx, tc, testMutationTarget(filepath.Join(root, "tree")), "tree", int64(len("child bytes")), nil, nil)
		},
		"link create": func(ctx context.Context, tc tools.ToolContext, root string) error {
			path := filepath.Join(root, "destination")
			pin, err := (ExecutionRevisions{}).CaptureDestination(ctx, tc, path)
			if err != nil {
				return err
			}
			return (&WriteTool{}).Link(ctx, LinkInput{Path: path, Target: "source.txt", Destination: pin}, tc)
		},
	}
	for name, operation := range operations {
		for _, journaled := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/no journal", true: "/journal"}[journaled], func(t *testing.T) {
				ctx, tc, change, stale := policyFenceFixture(t)
				root := tc.Source.Roots[0].Path
				seedPreparedPolicyFiles(t, root)
				journal := &policyFenceJournal{}
				if journaled {
					tc.Source.SourceMutations = journal
					journal.prepared = change
					// Worker branches use direct removal instead of project recovery.
					if name == "file remove" || name == "link remove" || name == "directory remove" {
						tc.Source.ProjectSourceBranch = sourcebranch.ID("worker")
					}
				} else {
					tc.Files.FileChangeReview = func(context.Context, []tools.FileChange) error {
						change()
						return nil
					}
				}
				if err := operation(ctx, tc, root); !errors.Is(err, stale) {
					t.Fatalf("operation = %v, want stale policy", err)
				}
				assertPreparedPolicyFiles(t, root)
				if journaled && (journal.intents != 1 || journal.committed != 0 || !errors.Is(journal.settled, stale)) {
					t.Fatalf("intent settlement: prepared=%d committed=%d result=%v", journal.intents, journal.committed, journal.settled)
				}
			})
		}
	}
}

func TestDirectoryReplacementRechecksPolicyBeforeDisplacingDestination(t *testing.T) {
	ctx, tc, change, stale := policyFenceFixture(t)
	root := tc.Source.Roots[0].Path
	seedPreparedPolicyFiles(t, root)
	destination := filepath.Join(root, "destination")
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	tc.Files.FileChangeReview = func(context.Context, []tools.FileChange) error {
		change()
		return nil
	}
	err := renameAgentPath(ctx, tc, testMutationTarget(filepath.Join(root, "tree")), testMutationTarget(destination), nil)
	if !errors.Is(err, stale) {
		t.Fatalf("directory replacement = %v, want stale policy", err)
	}
	for _, path := range []string{filepath.Join(root, "tree"), destination} {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			t.Fatalf("directory displaced after policy changed: %s, %v", path, err)
		}
	}
	body, err := os.ReadFile(filepath.Join(root, "tree", "child.txt"))
	if err != nil || string(body) != "child bytes" {
		t.Fatalf("source tree changed: %q, %v", body, err)
	}
}

func seedPreparedPolicyFiles(t *testing.T, root string) {
	t.Helper()
	if err := os.Mkdir(filepath.Join(root, "tree"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{"source.txt": "source bytes", "tree/child.txt": "child bytes"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("source.txt", filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
}

func assertPreparedPolicyFiles(t *testing.T, root string) {
	t.Helper()
	for path, body := range map[string]string{"source.txt": "source bytes", "tree/child.txt": "child bytes"} {
		got, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || string(got) != body {
			t.Fatalf("%s changed: %q, %v", path, got, err)
		}
		info, err := os.Stat(filepath.Join(root, path))
		if err != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("%s mode changed: %v, %v", path, info, err)
		}
	}
	if target, err := os.Readlink(filepath.Join(root, "alias")); err != nil || target != "source.txt" {
		t.Fatalf("link changed: %q, %v", target, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "destination")); !os.IsNotExist(err) {
		t.Fatalf("destination published: %v", err)
	}
}
