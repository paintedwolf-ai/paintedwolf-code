package security

import (
	"context"
	"errors"
	"fmt"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	lyexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// Modified tracked content exercises external-diff handling alongside the other executable config keys.
func TestHostileRepoGitJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("security: skipped under -short")
	}
	gittestsetup.Enable()
	if _, err := gitengine.BinaryPath(); err != nil {
		t.Skipf("bundled git missing — run ./task gitengine:fetch: %v", err)
	}
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	sentinels := plantHostileRepo(t, dir)

	mgr := git.NewManager()
	ctx := context.Background()

	if _, err := mgr.Status(ctx, dir); err != nil {
		t.Fatalf("Status: %v", err)
	}
	diff, err := mgr.Diff(ctx, dir, git.GitDiffOpts{})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	// A nonempty diff confirms the external-diff path was exercised.
	if !strings.Contains(diff, "seed.txt") {
		t.Fatalf("Diff produced no tracked change; the diff.external path was never exercised: %q", diff)
	}
	if _, err := mgr.DiffStat(ctx, dir, git.GitDiffOpts{}); err != nil {
		t.Fatalf("DiffStat: %v", err)
	}
	if _, err := mgr.Commit(ctx, dir, git.GitCommitOpts{Message: "hostile-commit", Paths: []string{"work.txt"}}); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := mgr.CreateBranch(ctx, dir, "topic"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := mgr.Checkout(ctx, dir, "topic"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if _, err := mgr.Log(ctx, dir, git.GitLogOpts{Limit: 5}); err != nil {
		t.Fatalf("Log: %v", err)
	}

	for _, path := range sentinels {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			raw, _ := os.ReadFile(path)
			t.Fatalf("sentinel executed: %s (%v): %s", path, err, raw)
		}
	}

	boundary := sandbox.NewBoundary(sandbox.Config{
		ProjectRootRequired: true,
		RejectSymlinkEscape: true,
	}, []sandbox.ToolProfile{{
		ID:    tools.DefaultToolProfileID,
		Tools: map[string]bool{"write": true, "edit": true},
	}})
	tctx := tools.ToolContext{
		Roots:              []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}},
		ActiveRootID:       "r1",
		Agent:              tools.DefaultToolProfileID,
		SessionID:          "hostile-repo",
		RepoFileCount:      10,
		RepoFileCountKnown: true,
	}
	_, err = projectpaths.ResolveWrite(ctx, boundary, tctx, ".git/hooks/pre-commit")
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "GIT_INTERNALS_WRITE_DENIED" {
		t.Fatalf("ResolveWrite(.git/hooks/pre-commit) = %v, want GIT_INTERNALS_WRITE_DENIED", err)
	}
}

func plantHostileRepo(t *testing.T, dir string) []string {
	t.Helper()
	ref, err := osexec.LookPath("git")
	if err != nil {
		t.Skip("host git required to plant the hostile fixture")
	}
	env := lyexec.LocalGitEnv()
	run := func(args ...string) {
		t.Helper()
		cmd := osexec.CommandContext(t.Context(), ref, append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("ref git %v: %v\n%s", args, err, out)
		}
	}

	// Seed a clean commit before planting payloads so host git does not fire
	// the hostile hooks during fixture construction.
	run("init")
	run("checkout", "-b", "main")
	testutil.FailErr(t, "write seed", os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed\n"), 0o644))
	run("add", "seed.txt")
	run("-c", "user.email=hostile@example.com", "-c", "user.name=Hostile", "commit", "-m", "seed")

	sentinelDir := filepath.Join(dir, "_sentinels")
	testutil.FailErr(t, "mkdir sentinels", os.MkdirAll(sentinelDir, 0o755))
	names := []string{
		"default-pre-commit",
		"default-post-checkout",
		"default-pre-push",
		"alt-pre-commit",
		"alt-post-checkout",
		"fsmonitor",
		"diff-external",
		"ssh-command",
		"credential-helper",
	}
	paths := make([]string, 0, len(names))
	for _, n := range names {
		paths = append(paths, filepath.Join(sentinelDir, n))
	}

	writeHook := func(path, sentinel string) {
		t.Helper()
		body := fmt.Sprintf("#!/bin/sh\necho ran > %q\nexit 0\n", sentinel)
		testutil.FailErr(t, "mkdir hook parent", os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "write hook", os.WriteFile(path, []byte(body), 0o755))
	}

	writeHook(filepath.Join(dir, ".git", "hooks", "pre-commit"), paths[0])
	writeHook(filepath.Join(dir, ".git", "hooks", "post-checkout"), paths[1])
	writeHook(filepath.Join(dir, ".git", "hooks", "pre-push"), paths[2])

	altHooks := filepath.Join(dir, ".githooks")
	writeHook(filepath.Join(altHooks, "pre-commit"), paths[3])
	writeHook(filepath.Join(altHooks, "post-checkout"), paths[4])

	fsmonitor := filepath.Join(dir, "_poison", "fsmonitor.sh")
	diffExt := filepath.Join(dir, "_poison", "diff-external.sh")
	sshCmd := filepath.Join(dir, "_poison", "ssh-command.sh")
	credHelper := filepath.Join(dir, "_poison", "credential-helper.sh")
	writeHook(fsmonitor, paths[5])
	writeHook(diffExt, paths[6])
	writeHook(sshCmd, paths[7])
	writeHook(credHelper, paths[8])

	cfgPath := filepath.Join(dir, ".git", "config")
	raw, err := os.ReadFile(cfgPath)
	testutil.FailErr(t, "read config", err)
	// Every repo-local key that names a program git would otherwise execute.
	poison := fmt.Sprintf(
		"\n[core]\n\thooksPath = .githooks\n\tfsmonitor = %s\n\tsshCommand = %s\n[diff]\n\texternal = %s\n[credential]\n\thelper = %s\n",
		fsmonitor, sshCmd, diffExt, credHelper)
	testutil.FailErr(t, "write poisoned config", os.WriteFile(cfgPath, append(raw, []byte(poison)...), 0o644))

	testutil.FailErr(t, "write attrs", os.WriteFile(
		filepath.Join(dir, ".gitattributes"),
		[]byte("*.dat filter=custom-undefined\n"),
		0o644,
	))
	testutil.FailErr(t, "write dat", os.WriteFile(filepath.Join(dir, "data.dat"), []byte("plain\n"), 0o644))
	testutil.FailErr(t, "write work", os.WriteFile(filepath.Join(dir, "work.txt"), []byte("dirty\n"), 0o644))
	// A tracked edit exercises external-diff handling.
	testutil.FailErr(t, "dirty tracked file", os.WriteFile(
		filepath.Join(dir, "seed.txt"), []byte("seed\nmodified\n"), 0o644))
	return paths
}

// Attributes can invoke arbitrarily named filter drivers during staging, requiring repository refusal.
func TestHostileRepoDeclaredFilterIsRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("security: skipped under -short")
	}
	gittestsetup.Enable()
	if _, err := gitengine.BinaryPath(); err != nil {
		t.Skipf("bundled git missing — run ./task gitengine:fetch: %v", err)
	}
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	sentinel := filepath.Join(t.TempDir(), "filter-ran")
	plantFilterRepo(t, dir, sentinel)

	mgr := git.NewManager()
	ctx := context.Background()

	_, err := mgr.Status(ctx, dir)
	var unsafeCfg *gitexec.UnsafeRepoConfigError
	if !errors.As(err, &unsafeCfg) {
		t.Fatalf("Status on a repo declaring a filter driver = %v, want *gitexec.UnsafeRepoConfigError", err)
	}
	if unsafeCfg.Code() != "GIT_REPO_CONFIG_UNSAFE" {
		t.Fatalf("Code = %q", unsafeCfg.Code())
	}
	if _, err := mgr.Commit(ctx, dir, git.GitCommitOpts{Message: "should not run", Paths: nil}); !errors.As(err, &unsafeCfg) {
		t.Fatalf("Commit = %v, want the same refusal", err)
	}
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Fatalf("filter driver executed: %s", sentinel)
	}

	payload, mErr := git.MarshalToolFailure(err)
	if !errors.Is(mErr, err) {
		t.Fatalf("serialized failure must retain the original refusal: %v", mErr)
	}
	if !strings.Contains(payload, `"code":"GIT_REPO_CONFIG_UNSAFE"`) {
		t.Fatalf("tool envelope missing structured code: %s", payload)
	}
}

// Repository filter configuration also applies when the attached root is a subdirectory.
func TestHostileRepoFilterIsRefusedFromASubdirectoryRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("security: skipped under -short")
	}
	gittestsetup.Enable()
	if _, err := gitengine.BinaryPath(); err != nil {
		t.Skipf("bundled git missing — run ./task gitengine:fetch: %v", err)
	}
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	sentinel := filepath.Join(t.TempDir(), "filter-ran")
	plantFilterRepo(t, dir, sentinel)

	root := filepath.Join(dir, "packages", "web")
	testutil.FailErr(t, "mkdir package root", os.MkdirAll(root, 0o755))
	testutil.FailErr(t, "write tracked", os.WriteFile(filepath.Join(root, "b.txt"), []byte("content\n"), 0o644))

	mgr := git.NewManager()
	ctx := context.Background()

	var unsafeCfg *gitexec.UnsafeRepoConfigError
	if _, err := mgr.Status(ctx, root); !errors.As(err, &unsafeCfg) {
		t.Fatalf("Status from a subdirectory root = %v, want *gitexec.UnsafeRepoConfigError", err)
	}
	if _, err := mgr.Diff(ctx, root, git.GitDiffOpts{}); !errors.As(err, &unsafeCfg) {
		t.Fatalf("Diff from a subdirectory root = %v, want the same refusal", err)
	}
	if _, err := mgr.Commit(ctx, root, git.GitCommitOpts{Message: "should not run", Paths: nil}); !errors.As(err, &unsafeCfg) {
		t.Fatalf("Commit from a subdirectory root = %v, want the same refusal", err)
	}
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Fatalf("filter driver executed from a subdirectory root: %s", sentinel)
	}
}

func plantFilterRepo(t *testing.T, dir, sentinel string) {
	t.Helper()
	ref, err := osexec.LookPath("git")
	if err != nil {
		t.Skip("host git required to plant the hostile fixture")
	}
	env := lyexec.LocalGitEnv()
	run := func(args ...string) {
		t.Helper()
		cmd := osexec.CommandContext(t.Context(), ref, append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		if out, runErr := cmd.CombinedOutput(); runErr != nil {
			t.Fatalf("ref git %v: %v\n%s", args, runErr, out)
		}
	}
	run("init")
	run("checkout", "-b", "main")

	payload := filepath.Join(dir, "_poison", "filter.sh")
	testutil.FailErr(t, "mkdir payload", os.MkdirAll(filepath.Dir(payload), 0o755))
	testutil.FailErr(t, "write payload", os.WriteFile(payload,
		[]byte(fmt.Sprintf("#!/bin/sh\necho ran > %q\ncat\n", sentinel)), 0o755))

	cfgPath := filepath.Join(dir, ".git", "config")
	raw, err := os.ReadFile(cfgPath)
	testutil.FailErr(t, "read config", err)
	stanza := fmt.Sprintf("\n[filter \"payload\"]\n\tclean = %s\n\tsmudge = cat\n", payload)
	testutil.FailErr(t, "write filter config", os.WriteFile(cfgPath, append(raw, []byte(stanza)...), 0o644))

	testutil.FailErr(t, "write attrs", os.WriteFile(filepath.Join(dir, ".gitattributes"),
		[]byte("*.txt filter=payload\n"), 0o644))
	testutil.FailErr(t, "write tracked", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("content\n"), 0o644))
}
