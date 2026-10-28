package gitcmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestRemoveTrackedFiles(t *testing.T) {
	for _, test := range []struct {
		name     string
		args     []string
		status   int
		retained bool
		output   string
	}{
		{"clean", []string{"rm", "--", "sub/b.txt"}, 0, false, "rm 'sub/b.txt'\n"},
		{"cached", []string{"rm", "--cached", "--", "sub/b.txt"}, 0, true, "rm 'sub/b.txt'\n"},
		{"force", []string{"rm", "-f", "--", "a.txt"}, 0, false, "rm 'a.txt'\n"},
		{"dry", []string{"rm", "--dry-run", "--", "sub/b.txt"}, 0, true, "rm 'sub/b.txt'\n"},
		{"quiet", []string{"rm", "-q", "--", "sub/b.txt"}, 0, false, ""},
		{"recursive", []string{"rm", "-r", "--", "sub"}, 0, false, "rm 'sub/b.txt'\n"},
		{"unmatched", []string{"rm", "--ignore-unmatch", "--", "absent"}, 0, true, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newApplyHarness(t)
			before := h.git("ls-files", "--stage")
			got := h.exec(append([]string{"git"}, test.args...)...)
			testutil.FailErr(t, "remove", got.err)
			if got.result.Status != test.status || got.stdout != test.output || got.stderr != "" {
				t.Fatalf("result %+v stdout %q stderr %q", got.result, got.stdout, got.stderr)
			}
			name := "sub/b.txt"
			if test.name == "force" {
				name = "a.txt"
			}
			if h.absent(name) == test.retained {
				t.Fatalf("retained=%v", !h.absent(name))
			}
			if test.name == "dry" || test.name == "unmatched" {
				if after := h.git("ls-files", "--stage"); after != before {
					t.Fatal("no-effect command changed index")
				}
				if got.result.EffectCertainty != toolcontract.NoEffect {
					t.Fatal("dry run reported effects")
				}
			} else if staged := h.git("diff", "--cached", "--name-only"); !strings.Contains(staged, "staged.txt") {
				t.Fatal("unrelated staging changed")
			}
		})
	}
}

func TestRemoveSafety(t *testing.T) {
	for _, test := range []struct {
		name   string
		staged bool
		work   string
		cached bool
		reason string
	}{
		{"local", false, "local\n", false, "local modifications"},
		{"staged", true, "index\n", false, "changes staged in the index"},
		{"both", true, "work\n", false, "staged content different from both the\nfile and the HEAD"},
		{"cached-both", true, "work\n", true, "staged content different from both the\nfile and the HEAD"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newApplyHarness(t)
			if test.staged {
				h.write("a.txt", "index\n")
				h.git("add", "a.txt")
			}
			h.write("a.txt", test.work)
			before := h.git("ls-files", "--stage")
			args := []string{"git", "rm"}
			if test.cached {
				args = append(args, "--cached")
			}
			got := h.exec(append(args, "--", "a.txt")...)
			testutil.FailErr(t, "safety refusal", got.err)
			if got.result.Status != 1 || !strings.Contains(got.stderr, test.reason) || got.stdout != "" || got.result.EffectCertainty != toolcontract.NoEffect {
				t.Fatalf("got %+v %q %q", got.result, got.stdout, got.stderr)
			}
			if h.read("a.txt") != test.work || h.git("ls-files", "--stage") != before {
				t.Fatal("refusal had effects")
			}
		})
	}
}

func TestMoveTracksIndexContentAndSession(t *testing.T) {
	h := newApplyHarness(t)
	got := h.exec("git", "mv", "-v", "--", "a.txt", "renamed.txt")
	testutil.FailErr(t, "move", got.err)
	if got.result.Status != 0 || got.stdout != "Renaming a.txt to renamed.txt\n" || !h.absent("a.txt") || h.read("renamed.txt") != "one\nTWO\nthree\n" {
		t.Fatalf("move %+v %q %q", got.result, got.stdout, got.stderr)
	}
	if got := h.git("show", ":renamed.txt"); got != "one\ntwo\nthree\n" {
		t.Fatalf("index changed saved content %q", got)
	}
	if staged := h.git("diff", "--cached", "--name-status"); !strings.Contains(staged, "R100\ta.txt\trenamed.txt") || !strings.Contains(staged, "staged.txt") {
		t.Fatalf("staging %q", staged)
	}
	paths := sessionStaged(h.owners, h.tc.ChatSessionID(), h.root)
	if strings.Join(paths, ",") != "a.txt,renamed.txt" {
		t.Fatalf("session staging %q", paths)
	}
}

func TestMoveDirectoryAndSkip(t *testing.T) {
	h := newApplyHarness(t)
	h.write("sub/untracked", "keep\n")
	testutil.FailErr(t, "destination", os.Mkdir(filepath.Join(h.root, "destination"), 0755))
	got := h.exec("git", "mv", "-k", "--", "absent", "sub", "destination")
	testutil.FailErr(t, "move", got.err)
	if got.result.Status != 0 || !h.absent("sub") || h.read("destination/sub/b.txt") != "bee\n" || h.read("destination/sub/untracked") != "keep\n" {
		t.Fatalf("move %+v %q", got.result, got.stderr)
	}
	if staged := h.git("diff", "--cached", "--name-status"); !strings.Contains(staged, "sub/b.txt\tdestination/sub/b.txt") {
		t.Fatalf("staging %q", staged)
	}
}

func TestFileMutationDenialAndLaterIndexFailure(t *testing.T) {
	for _, command := range []string{"rm", "mv"} {
		for _, later := range []bool{false, true} {
			t.Run(command+map[bool]string{false: "-denied", true: "-index-failure"}[later], func(t *testing.T) {
				h := newApplyHarness(t)
				original := h.git("ls-files", "--stage")
				h.tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
					if !later || len(changes) > 0 && changes[0].Preview.Target == "index" {
						return &toolrejection.ToolReject{Code: "GIT_OPERATION_PRECONDITION", Data: map[string]any{"reason": "fixture_denial"}}
					}
					return nil
				}
				args := []string{"git", command, "--", "sub/b.txt"}
				if command == "mv" {
					args = append(args, "moved.txt")
				}
				got := h.exec(args...)
				if got.err == nil {
					t.Fatal("expected review failure")
				}
				certainty := toolcontract.NoEffect
				if later {
					certainty = toolcontract.EffectCompleted
				}
				if got.result.EffectCertainty != certainty || h.absent("sub/b.txt") != later || h.git("ls-files", "--stage") != original {
					t.Fatalf("partial result %+v absent=%v", got.result, h.absent("sub/b.txt"))
				}
			})
		}
	}
}

func TestFileMutationDryRunAndPathRefusals(t *testing.T) {
	h := newApplyHarness(t)
	original := h.git("ls-files", "--stage")
	got := h.exec("git", "mv", "-n", "--", "sub/b.txt", "next.txt")
	testutil.FailErr(t, "dry move", got.err)
	if got.stdout != "Checking rename of 'sub/b.txt' to 'next.txt'\nRenaming sub/b.txt to next.txt\n" || got.result.EffectCertainty != toolcontract.NoEffect || h.absent("sub/b.txt") || h.git("ls-files", "--stage") != original {
		t.Fatalf("dry move %+v %q", got.result, got.stdout)
	}
	for _, args := range [][]string{{"git", "rm", "--", ":(glob)*"}, {"git", "mv", "--", "sub/b.txt", "../outside"}, {"git", "rm", "--", "../outside"}} {
		got := h.exec(args...)
		if got.err == nil || got.result.EffectCertainty != toolcontract.NoEffect {
			t.Fatalf("path refusal %+v %v", got.result, got.err)
		}
	}
}
