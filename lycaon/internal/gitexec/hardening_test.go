package gitexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// appendRepoConfig writes fixture stanzas without invoking Git.
func appendRepoConfig(t *testing.T, dir, stanza string) {
	t.Helper()
	path := filepath.Join(dir, ".git", "config")
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read repo config", err)
	testutil.FailErr(t, "write repo config", os.WriteFile(path, append(raw, []byte(stanza)...), 0o600))
}

func sentinelScript(t *testing.T, path, sentinel string) {
	t.Helper()
	body := "#!/bin/sh\necho ran > '" + sentinel + "'\ncat\n"
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "write script", os.WriteFile(path, []byte(body), 0o755))
}

func assertNoSentinel(t *testing.T, path, what string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s executed (sentinel %s exists)", what, path)
	}
}

func TestRepoLocalDiffExternalNeverRuns(t *testing.T) {
	useBundledGit(t)
	dir := t.TempDir()
	initRepo(t, dir)
	writeFile(t, dir, "tracked.txt", "one\n")
	commitWithIdentity(t, dir, "seed")

	sentinel := filepath.Join(t.TempDir(), "diff-external-ran")
	script := filepath.Join(dir, "payload.sh")
	sentinelScript(t, script, sentinel)
	appendRepoConfig(t, dir, "\n[diff]\n\texternal = "+script+"\n")

	writeFile(t, dir, "tracked.txt", "one\ntwo\n")

	for _, args := range [][]string{
		{"diff"},
		{"diff", "--cached"},
		{"log", "-p", "-1"},
		{"show", "HEAD"},
	} {
		out, code, err := Run(t.Context(), dir, args, Opts{})
		if err != nil || code > 1 {
			t.Fatalf("git %v: err=%v code=%d out=%s", args, err, code, out)
		}
		assertNoSentinel(t, sentinel, "git "+strings.Join(args, " ")+" diff.external")
	}

	out, _, err := Run(t.Context(), dir, []string{"diff"}, Opts{})
	testutil.FailErr(t, "diff", err)
	if !strings.Contains(string(out), "+two") {
		t.Fatalf("diff lost its content: %s", out)
	}
}

func TestRepoLocalDiffTextconvNeverRuns(t *testing.T) {
	useBundledGit(t)
	dir := t.TempDir()
	initRepo(t, dir)
	writeFile(t, dir, "tracked.txt", "one\n")
	writeFile(t, dir, ".gitattributes", "* diff=payload\n")
	commitWithIdentity(t, dir, "seed")

	sentinel := filepath.Join(t.TempDir(), "textconv-ran")
	script := filepath.Join(dir, "payload.sh")
	sentinelScript(t, script, sentinel)
	appendRepoConfig(t, dir, "\n[diff \"payload\"]\n\ttextconv = "+script+"\n")

	writeFile(t, dir, "tracked.txt", "one\ntwo\n")

	for _, args := range [][]string{
		{"diff"},
		{"diff", "--cached"},
		{"log", "-p", "-1"},
		{"show", "HEAD"},
		{"blame", "tracked.txt"},
	} {
		out, code, err := Run(t.Context(), dir, args, Opts{})
		if err != nil || code > 1 {
			t.Fatalf("git %v: err=%v code=%d out=%s", args, err, code, out)
		}
		assertNoSentinel(t, sentinel, "git "+strings.Join(args, " ")+" diff.<name>.textconv")
	}

	out, _, err := Run(t.Context(), dir, []string{"diff"}, Opts{})
	testutil.FailErr(t, "diff", err)
	if !strings.Contains(string(out), "+two") {
		t.Fatalf("diff lost its content: %s", out)
	}
}

func TestDiffDriverFlagPlacement(t *testing.T) {
	both := []string{"--no-ext-diff", "--no-textconv"}
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"diff", []string{"diff", "--", "a.txt"}, []string{"diff", "--no-ext-diff", "--no-textconv", "--", "a.txt"}},
		{"log", []string{"log", "-p"}, []string{"log", "--no-ext-diff", "--no-textconv", "-p"}},
		{"blame", []string{"blame", "a.txt"}, []string{"blame", "--no-ext-diff", "--no-textconv", "a.txt"}},
		{"stash show", []string{"stash", "show"}, append([]string{"stash", "show"}, both...)},
		{"stash push untouched", []string{"stash", "push"}, []string{"stash", "push"}},
		{"status untouched", []string{"status", "--porcelain"}, []string{"status", "--porcelain"}},
		{"both already present", []string{"diff", "--no-ext-diff", "--no-textconv"}, []string{"diff", "--no-ext-diff", "--no-textconv"}},
		{"one already present", []string{"diff", "--no-ext-diff"}, []string{"diff", "--no-textconv", "--no-ext-diff"}},
		{"pathspec named like a flag", []string{"diff", "--", "--no-textconv"}, []string{"diff", "--no-ext-diff", "--no-textconv", "--", "--no-textconv"}},
		{"empty", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := diffDriverFlagArgs(tc.in)
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("diffDriverFlagArgs(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRepoLocalCredentialHelperReset(t *testing.T) {
	useBundledGit(t)
	SetHostConfig(stubHostConfig{helper: "osxkeychain"})
	executablePath = func() (string, error) { return "/Applications/Painted Wolf Code.app/Contents/MacOS/pw", nil }
	t.Cleanup(func() { executablePath = os.Executable })
	dir := t.TempDir()
	initRepo(t, dir)
	appendRepoConfig(t, dir, "\n[credential]\n\thelper = \"!repo-local-payload\"\n")

	argv, err := buildArgv(t.Context(), dir, Opts{
		Profile:   ProfileNetwork,
		RemoteURL: "https://example.com/r.git",
	}, []string{"fetch"})
	testutil.FailErr(t, "buildArgv", err)

	var helpers []string
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-c" && strings.HasPrefix(argv[i+1], "credential.helper=") {
			helpers = append(helpers, strings.TrimPrefix(argv[i+1], "credential.helper="))
		}
	}
	wantHelper := "!'/Applications/Painted Wolf Code.app/Contents/MacOS/pw' git-credential-osxkeychain"
	if len(helpers) != 2 || helpers[0] != "" || helpers[1] != wantHelper {
		t.Fatalf("credential.helper values = %q, want reset then resolved helper", helpers)
	}

	out, code, err := Run(t.Context(), dir, []string{"config", "--get-all", "credential.helper"}, Opts{})
	if err != nil || code != 0 {
		t.Fatalf("config --get-all: err=%v code=%d out=%s", err, code, out)
	}
	lines := strings.Fields(string(out))
	if len(lines) == 0 || lines[0] != "!repo-local-payload" {
		t.Fatalf("fixture did not declare a repo-local helper: %q", out)
	}
}

func TestRepoLocalSSHCommandOverridden(t *testing.T) {
	useBundledGit(t)
	SetHostConfig(nil)
	dir := t.TempDir()
	initRepo(t, dir)
	appendRepoConfig(t, dir, "\n[core]\n\tsshCommand = /payload/ssh\n")

	out, code, err := Run(t.Context(), dir, []string{"config", "--get", "core.sshCommand"}, Opts{})
	if err != nil || code != 0 {
		t.Fatalf("config --get: err=%v code=%d out=%s", err, code, out)
	}
	if got := strings.TrimSpace(string(out)); got != defaultSSHCommand {
		t.Fatalf("core.sshCommand = %q, want %q", got, defaultSSHCommand)
	}
}

func TestUserSSHCommandRestored(t *testing.T) {
	useBundledGit(t)
	SetHostConfig(stubHostConfig{ssh: "ssh -i /home/user/.ssh/id_work"})
	t.Cleanup(func() { SetHostConfig(nil) })

	prefix, err := neutralizeArgs(resolveSSHCommand(t.Context()))
	testutil.FailErr(t, "neutralizeArgs", err)
	found := false
	for i := 0; i+1 < len(prefix); i++ {
		if prefix[i] == "-c" && prefix[i+1] == "core.sshCommand=ssh -i /home/user/.ssh/id_work" {
			found = true
		}
	}
	if !found {
		t.Fatalf("user core.sshCommand not restored: %q", prefix)
	}
}

func TestUnsafeRepoConfigRefused(t *testing.T) {
	cases := []struct {
		name   string
		stanza string
		wantIn string
	}{
		{"filter driver", "\n[filter \"crypt\"]\n\tclean = /payload/clean\n\tsmudge = cat\n", "filter.crypt"},
		{"filter process", "\n[filter \"nb\"]\n\tprocess = /payload/proc\n", "filter.nb"},
		{"git proxy", "\n[core]\n\tgitProxy = /payload/proxy\n", "core.gitProxy"},
		{"filter name differing only in case", "\n[filter \"LFS\"]\n\tclean = /payload/clean\n", "filter.LFS"},
		{"merge driver", "\n[merge \"crypt\"]\n\tdriver = /payload/merge %O %A %B\n", "merge.crypt"},
		{"merge driver with a dotted name", "\n[merge \"a.b\"]\n\tdriver = /payload/merge\n", "merge.a.b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useBundledGit(t)
			dir := t.TempDir()
			initRepo(t, dir)
			appendRepoConfig(t, dir, tc.stanza)

			_, _, err := Run(t.Context(), dir, []string{"status", "--porcelain"}, Opts{})
			var unsafeCfg *UnsafeRepoConfigError
			if !errors.As(err, &unsafeCfg) {
				t.Fatalf("err = %v (%T), want *UnsafeRepoConfigError", err, err)
			}
			if unsafeCfg.Code() != "GIT_REPO_CONFIG_UNSAFE" {
				t.Fatalf("Code = %q", unsafeCfg.Code())
			}
			if !strings.Contains(unsafeCfg.KeyList(), tc.wantIn) {
				t.Fatalf("KeyList = %q, want it to name %q", unsafeCfg.KeyList(), tc.wantIn)
			}
		})
	}
}

func TestLFSFilterConfigAllowed(t *testing.T) {
	useBundledGit(t)
	dir := t.TempDir()
	initRepo(t, dir)
	appendRepoConfig(t, dir, "\n[filter \"lfs\"]\n\tclean = git-lfs clean -- %f\n\tsmudge = git-lfs smudge -- %f\n\tprocess = git-lfs filter-process\n\trequired = true\n")

	out, code, err := Run(t.Context(), dir, []string{"status", "--porcelain"}, Opts{})
	if err != nil || code != 0 {
		t.Fatalf("status on an LFS repo: err=%v code=%d out=%s", err, code, out)
	}
}

func TestRepoConfigAuditSeesLaterEdits(t *testing.T) {
	useBundledGit(t)
	dir := t.TempDir()
	initRepo(t, dir)

	if _, _, err := Run(t.Context(), dir, []string{"status", "--porcelain"}, Opts{}); err != nil {
		t.Fatalf("clean repo must run: %v", err)
	}
	appendRepoConfig(t, dir, "\n[filter \"planted\"]\n\tclean = /payload/clean\n")

	_, _, err := Run(t.Context(), dir, []string{"status", "--porcelain"}, Opts{})
	var unsafeCfg *UnsafeRepoConfigError
	if !errors.As(err, &unsafeCfg) {
		t.Fatalf("err = %v, want the audit to notice the planted driver", err)
	}
}

func TestUnsafeRepoConfigRefusedFromSubdirectory(t *testing.T) {
	useBundledGit(t)
	dir := t.TempDir()
	initRepo(t, dir)
	appendRepoConfig(t, dir, "\n[filter \"crypt\"]\n\tclean = /payload/clean\n")

	nested := filepath.Join(dir, "packages", "web", "src")
	testutil.FailErr(t, "mkdir nested", os.MkdirAll(nested, 0o755))

	for _, args := range [][]string{
		{"status", "--porcelain"},
		{"diff"},
		{"checkout", "--", "."},
	} {
		_, _, err := Run(t.Context(), nested, args, Opts{})
		var unsafeCfg *UnsafeRepoConfigError
		if !errors.As(err, &unsafeCfg) {
			t.Fatalf("git %v from a subdirectory = %v (%T), want *UnsafeRepoConfigError", args, err, err)
		}
		if !strings.Contains(unsafeCfg.KeyList(), "filter.crypt") {
			t.Fatalf("KeyList = %q, want it to name filter.crypt", unsafeCfg.KeyList())
		}
	}
}

func TestCleanRepoRunsFromSubdirectory(t *testing.T) {
	useBundledGit(t)
	dir := t.TempDir()
	initRepo(t, dir)
	nested := filepath.Join(dir, "packages", "web")
	testutil.FailErr(t, "mkdir nested", os.MkdirAll(nested, 0o755))

	if _, _, err := Run(t.Context(), nested, []string{"status", "--porcelain"}, Opts{}); err != nil {
		t.Fatalf("clean repo must run from a subdirectory: %v", err)
	}
}

func TestNonRepositoryDirectoryIsNotAudited(t *testing.T) {
	useBundledGit(t)
	dir := t.TempDir()

	_, _, err := Run(t.Context(), dir, []string{"rev-parse", "--show-toplevel"}, Opts{})
	var unsafeCfg *UnsafeRepoConfigError
	if errors.As(err, &unsafeCfg) {
		t.Fatalf("a plain directory was refused as an unsafe repository: %v", err)
	}
}

func TestRepoConfigAuditSeesLaterEditsFromSubdirectory(t *testing.T) {
	useBundledGit(t)
	dir := t.TempDir()
	initRepo(t, dir)
	nested := filepath.Join(dir, "packages", "web")
	testutil.FailErr(t, "mkdir nested", os.MkdirAll(nested, 0o755))

	if _, _, err := Run(t.Context(), dir, []string{"status", "--porcelain"}, Opts{}); err != nil {
		t.Fatalf("clean repo must run: %v", err)
	}
	appendRepoConfig(t, dir, "\n[filter \"planted\"]\n\tclean = /payload/clean\n")

	_, _, err := Run(t.Context(), nested, []string{"status", "--porcelain"}, Opts{})
	var unsafeCfg *UnsafeRepoConfigError
	if !errors.As(err, &unsafeCfg) {
		t.Fatalf("err = %v, want the cached pass to have been invalidated by the edit", err)
	}
}

func TestClassifyRepoConfigKeys(t *testing.T) {
	cases := []struct {
		name string
		keys []string
		want []string
	}{
		{"clean repo", []string{"core.bare", "remote.origin.url"}, nil},
		{"lfs allowed", []string{"filter.lfs.clean", "filter.lfs.required"}, nil},
		{"case-sensitive subsection", []string{"filter.LFS.clean"}, []string{"filter.LFS"}},
		{"dotted subsection", []string{"filter.my.tool.smudge"}, []string{"filter.my.tool"}},
		{"non-command filter key ignored", []string{"filter.crypt.required"}, nil},
		{"proxy", []string{"core.gitproxy"}, []string{"core.gitProxy"}},
		{"sorted and deduped", []string{"filter.b.clean", "filter.a.clean", "filter.a.smudge"}, []string{"filter.a", "filter.b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyRepoConfigKeys([]byte(strings.Join(tc.keys, "\x00")))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("classifyRepoConfigKeys(%q) = %q, want %q", tc.keys, got, tc.want)
			}
		})
	}
}

func TestResolveIdentityPrefersRepoThenGlobal(t *testing.T) {
	useBundledGit(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("GIT_CONFIG_GLOBAL", "")
	testutil.FailErr(t, "write global config", os.WriteFile(
		filepath.Join(home, ".gitconfig"),
		[]byte("[user]\n\tname = Global User\n\temail = global@example.com\n"),
		0o600,
	))

	dir := t.TempDir()
	initRepo(t, dir)

	id, ok := ResolveIdentity(t.Context(), dir)
	if !ok || id.Name != "Global User" || id.Email != "global@example.com" {
		t.Fatalf("global identity = %+v ok=%v", id, ok)
	}

	appendRepoConfig(t, dir, "\n[user]\n\tname = Repo User\n\temail = repo@example.com\n")
	id, ok = ResolveIdentity(t.Context(), dir)
	if !ok || id.Name != "Repo User" || id.Email != "repo@example.com" {
		t.Fatalf("repo identity = %+v ok=%v", id, ok)
	}
}

func TestResolveIdentityIncompleteIsNotAnIdentity(t *testing.T) {
	useBundledGit(t)
	dir := t.TempDir()
	initRepo(t, dir)
	appendRepoConfig(t, dir, "\n[user]\n\tname = Only A Name\n")

	if id, ok := ResolveIdentity(t.Context(), dir); ok {
		t.Fatalf("half an identity must not resolve: %+v", id)
	}
}

func TestParseIdentity(t *testing.T) {
	id := parseIdentity([]byte("user.email ada@example.com\nuser.name Ada Lovelace\n"))
	if id.Name != "Ada Lovelace" || id.Email != "ada@example.com" {
		t.Fatalf("parseIdentity = %+v", id)
	}
	if got := parseIdentity([]byte("user.name\n")); got.Name != "" {
		t.Fatalf("valueless key must not set a name: %+v", got)
	}
}

func TestRemoteNameFromArgs(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"push", "origin", "main"}, "origin"},
		{[]string{"push", "--force-with-lease", "upstream"}, "upstream"},
		{[]string{"fetch", "--all"}, ""},
		{[]string{"pull", "--ff-only"}, ""},
		{[]string{"ls-remote", "--heads", "origin", "main"}, "origin"},
		{[]string{"clone", "--depth", "1", "--branch", "main", "--", "https://example.com/r.git", "dest"}, "https://example.com/r.git"},
		{[]string{"clone", "--", "/local/path/repo", "dest"}, ""},
		{[]string{"status"}, ""},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := remoteNameFromArgs(tc.args); got != tc.want {
			t.Fatalf("remoteNameFromArgs(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

func TestLooksLikeURL(t *testing.T) {
	for _, s := range []string{"https://example.com/r.git", "git@github.com:owner/repo.git", "ssh://host/x"} {
		if !looksLikeURL(s) {
			t.Fatalf("looksLikeURL(%q) = false", s)
		}
	}
	for _, s := range []string{"origin", "upstream", "my/remote"} {
		if looksLikeURL(s) {
			t.Fatalf("looksLikeURL(%q) = true", s)
		}
	}
}

func TestRemoteURLResolvedForPushPull(t *testing.T) {
	useBundledGit(t)
	seen := &recordingHostConfig{}
	SetHostConfig(seen)
	t.Cleanup(func() { SetHostConfig(nil) })

	dir := t.TempDir()
	initRepo(t, dir)
	out, code, err := Run(t.Context(), dir, []string{"remote", "add", "origin", "https://example.com/owner/repo.git"}, Opts{})
	if err != nil || code != 0 {
		t.Fatalf("remote add: err=%v code=%d out=%s", err, code, out)
	}

	_, err = buildArgvForTest(t, dir, Opts{Profile: ProfileNetwork}, []string{"pull", "--ff-only"})
	testutil.FailErr(t, "buildArgv", err)
	if seen.lastURL != "https://example.com/owner/repo.git" {
		t.Fatalf("helper resolved for %q, want the repository's origin URL", seen.lastURL)
	}
}

func buildArgvForTest(t *testing.T, dir string, opts Opts, args []string) ([]string, error) {
	t.Helper()
	return buildArgv(t.Context(), dir, opts, args)
}

type recordingHostConfig struct {
	lastURL string
}

func (r *recordingHostConfig) HelperFor(_ context.Context, remoteURL string) string {
	r.lastURL = remoteURL
	return ""
}

func (r *recordingHostConfig) SSHCommand(context.Context) string { return "" }

func TestHostConfigReadsOnlyTheUsersGlobalFile(t *testing.T) {
	useBundledGit(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("GIT_CONFIG_GLOBAL", "")
	testutil.FailErr(t, "write global config", os.WriteFile(
		filepath.Join(home, ".gitconfig"),
		[]byte("[credential \"https://example.com\"]\n\thelper = users-own-helper\n"),
		0o600,
	))

	repo := t.TempDir()
	initRepo(t, repo)
	appendRepoConfig(t, repo, "\n[credential \"https://example.com\"]\n\thelper = \"!repo-local-payload\"\n")
	t.Chdir(repo)

	got := HostConfigResolver().HelperFor(t.Context(), "https://example.com/owner/repo.git")
	if got != "users-own-helper" {
		t.Fatalf("HelperFor = %q, want the user's own global helper", got)
	}
}

func TestInitUsesEmptyTemplateDir(t *testing.T) {
	useBundledGit(t)
	dir := t.TempDir()
	out, code, err := Run(t.Context(), dir, []string{"init"}, Opts{})
	if err != nil || code != 0 {
		t.Fatalf("init: err=%v code=%d out=%s", err, code, out)
	}
	if strings.Contains(string(out), "templates not found") {
		t.Fatalf("init warned about missing templates: %s", out)
	}
	entries, err := os.ReadDir(filepath.Join(dir, ".git", "hooks"))
	if err == nil && len(entries) > 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("init copied hooks from a template dir: %v", names)
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/Applications/Painted Wolf Code.app/git-lfs": `'/Applications/Painted Wolf Code.app/git-lfs'`,
		"/plain/path": `'/plain/path'`,
		"/it's/here":  `'/it'\''s/here'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Fatalf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
