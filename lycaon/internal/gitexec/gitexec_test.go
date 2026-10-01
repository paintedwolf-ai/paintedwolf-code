package gitexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func useBundledGit(t *testing.T) string {
	t.Helper()
	bin := bundledGitBinary(t)
	binaryPathFn = func() (string, error) { return bin, nil }
	t.Cleanup(func() {
		binaryPathFn = gitengine.BinaryPath
		emptyHooksDirFn = gitengine.EmptyHooksDir
		lfsPathFn = resolveLFSPath
		SetHostConfig(nil)
		agentSocketResolver = defaultAgentSocket
		executablePath = os.Executable
	})
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir()) // isolate ~/.gitconfig
	return bin
}

func bundledGitBinary(t *testing.T) string {
	t.Helper()
	root := testutil.CheckoutRoot(t)
	engineRoot := filepath.Join(root, "lycaon-den", "src-tauri", "engine-root", "gitengine")
	bin := filepath.Join(engineRoot, "bin", "git")
	if runtime.GOOS == "windows" {
		bin = filepath.Join(engineRoot, "cmd", "git.exe")
	}
	st, err := os.Stat(bin)
	if err != nil || st.IsDir() || (runtime.GOOS != "windows" && st.Mode()&0o111 == 0) {
		t.Skip("bundled git missing — run ./task gitengine:fetch")
	}
	return bin
}

func bundledGitLFSBinary(t *testing.T) string {
	t.Helper()
	git := bundledGitBinary(t)
	if runtime.GOOS == "windows" {
		root := filepath.Dir(filepath.Dir(git))
		return filepath.Join(root, "mingw64", "libexec", "git-core", "git-lfs.exe")
	}
	return filepath.Join(filepath.Dir(git), "git-lfs")
}

func initRepo(t *testing.T, dir string) {
	t.Helper()
	out, code, err := Run(t.Context(), dir, []string{"init"}, Opts{})
	if err != nil || code != 0 {
		t.Fatalf("git init: err=%v code=%d out=%s", err, code, out)
	}
}

func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "write", os.WriteFile(path, []byte(body), 0o644))
}

func commitWithIdentity(t *testing.T, dir, msg string) {
	t.Helper()
	out, code, err := Run(t.Context(), dir, []string{"add", "-A"}, Opts{})
	if err != nil || code != 0 {
		t.Fatalf("git add: err=%v code=%d out=%s", err, code, out)
	}
	out, code, err = Run(t.Context(), dir, []string{"commit", "-m", msg}, Opts{
		Identity: &Identity{Name: "Test User", Email: "test@example.com"},
	})
	if err != nil || code != 0 {
		t.Fatalf("git commit: err=%v code=%d out=%s", err, code, out)
	}
}

func fileSHA(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read "+path, err)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "write hook", os.WriteFile(path, []byte(body), 0o755))
}

func TestNeutralizePrefixOrder(t *testing.T) {
	useBundledGit(t)

	prefix, err := neutralizeArgs(defaultSSHCommand)
	testutil.FailErr(t, "neutralizeArgs", err)
	if len(prefix) != len(neutralizePrefixKeys)*2 {
		t.Fatalf("prefix len=%d, want %d", len(prefix), len(neutralizePrefixKeys)*2)
	}
	for i, key := range neutralizePrefixKeys {
		if prefix[i*2] != "-c" {
			t.Fatalf("prefix[%d]=%q, want -c", i*2, prefix[i*2])
		}
		got := prefix[i*2+1]
		if !strings.HasPrefix(got, key+"=") {
			t.Fatalf("prefix entry %d = %q, want key %s=", i, got, key)
		}
	}

	argv, err := buildArgv(t.Context(), t.TempDir(), Opts{
		ExtraConfig: []string{"foo.bar=baz", "a.b=c"},
	}, []string{"status", "--porcelain"})
	testutil.FailErr(t, "buildArgv", err)

	wantTail := append(lfsFilterArgs(), "-c", "foo.bar=baz", "-c", "a.b=c", "status", "--porcelain")
	gotTail := argv[len(prefix):]
	if strings.Join(gotTail, "\x00") != strings.Join(wantTail, "\x00") {
		t.Fatalf("argv after prefix =\n%q\nwant\n%q", gotTail, wantTail)
	}
	if argv[0] != "-c" || !strings.HasPrefix(argv[1], "core.hooksPath=") {
		t.Fatal("caller args must not precede neutralization prefix")
	}
}

func TestLFSFilterPrefixWhenPresent(t *testing.T) {
	useBundledGit(t)
	lfs := bundledGitLFSBinary(t)
	st, err := os.Stat(lfs)
	if err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
		t.Skip("bundled git-lfs missing — run ./task gitengine:fetch")
	}
	lfsPathFn = func() (string, error) { return lfs, nil }
	t.Cleanup(func() { lfsPathFn = resolveLFSPath })

	args := lfsFilterArgs()
	if len(args) != 8 {
		t.Fatalf("lfsFilterArgs len=%d, want 8", len(args))
	}
	// Filter values are command lines and may contain spaces.
	q := "'" + lfs + "'"
	want := []string{
		"filter.lfs.clean=" + q + " clean -- %f",
		"filter.lfs.smudge=" + q + " smudge -- %f",
		"filter.lfs.process=" + q + " filter-process",
		"filter.lfs.required=true",
	}
	for i, w := range want {
		if args[i*2] != "-c" || args[i*2+1] != w {
			t.Fatalf("lfsFilterArgs[%d]=%q %q, want -c %q", i, args[i*2], args[i*2+1], w)
		}
	}

	argv, err := buildArgv(t.Context(), t.TempDir(), Opts{}, []string{"status"})
	testutil.FailErr(t, "buildArgv", err)
	prefix, err := neutralizeArgs(defaultSSHCommand)
	testutil.FailErr(t, "neutralizeArgs", err)
	got := argv[len(prefix) : len(prefix)+len(args)]
	if strings.Join(got, "\x00") != strings.Join(args, "\x00") {
		t.Fatalf("LFS filters must follow neutralize prefix:\ngot %q\nwant %q", got, args)
	}
}

func TestBundledToolPathsByOperatingSystem(t *testing.T) {
	t.Parallel()
	root := filepath.Join("root", "engine")
	if got, want := gitExecPathForOS(filepath.Join(root, "bin", "git"), "linux"), filepath.Join(root, "libexec", "git-core"); got != want {
		t.Fatalf("Linux git exec path = %q, want %q", got, want)
	}
	if got, want := gitExecPathForOS(filepath.Join(root, "cmd", "git.exe"), "windows"), filepath.Join(root, "mingw64", "libexec", "git-core"); got != want {
		t.Fatalf("Windows git exec path = %q, want %q", got, want)
	}
	if got, want := shellQuoteForOS(`C:\Program Files\Painted Wolf\git-lfs.exe`, "windows"), `'C:/Program Files/Painted Wolf/git-lfs.exe'`; got != want {
		t.Fatalf("Windows LFS filter path = %q, want %q", got, want)
	}
}

func TestBundledRuntimeEnvironmentByOperatingSystem(t *testing.T) {
	t.Parallel()
	root := filepath.Join("root", "engine")
	linux := bundledRuntimeEnvForOS(filepath.Join(root, "bin", "git"), "linux", "/host/bin")
	wantLinux := []string{
		"PREFIX=" + root,
		"GIT_SSL_CAINFO=" + filepath.Join(root, "ssl", "cacert.pem"),
	}
	if strings.Join(linux, "\x00") != strings.Join(wantLinux, "\x00") {
		t.Fatalf("Linux bundled runtime env = %q, want %q", linux, wantLinux)
	}
	windows := bundledRuntimeEnvForOS(filepath.Join(root, "cmd", "git.exe"), "windows", `C:\Windows\System32`)
	wantWindows := "PATH=" + filepath.Join(root, "mingw64", "bin") + ";" +
		filepath.Join(root, "usr", "bin") + `;C:\Windows\System32`
	if len(windows) != 1 || windows[0] != wantWindows {
		t.Fatalf("Windows bundled runtime env = %q, want %q", windows, wantWindows)
	}
}

func TestLFSFilterOmittedWhenAbsent(t *testing.T) {
	useBundledGit(t)
	lfsPathFn = func() (string, error) {
		return "", &gitengine.UnavailableError{Reason: "missing"}
	}
	t.Cleanup(func() { lfsPathFn = resolveLFSPath })

	if got := lfsFilterArgs(); len(got) != 0 {
		t.Fatalf("lfsFilterArgs = %v, want nil when LFS absent", got)
	}
	argv, err := buildArgv(t.Context(), t.TempDir(), Opts{}, []string{"status"})
	testutil.FailErr(t, "buildArgv", err)
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-c" && strings.HasPrefix(argv[i+1], "filter.lfs.") {
			t.Fatalf("absent LFS must omit filter.lfs.*: %v", argv)
		}
	}
}

func TestEngineUnavailablePropagates(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	binaryPathFn = func() (string, error) {
		return "", &gitengine.UnavailableError{Reason: "missing"}
	}
	t.Cleanup(func() { binaryPathFn = gitengine.BinaryPath })

	dir := t.TempDir()
	_, _, err := Run(t.Context(), dir, []string{"status"}, Opts{})
	var ue *gitengine.UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v (%T), want *gitengine.UnavailableError", err, err)
	}
	if ue.Reason != "missing" {
		t.Fatalf("Reason = %q, want missing", ue.Reason)
	}
}

func TestNilHostConfig(t *testing.T) {
	useBundledGit(t)
	SetHostConfig(nil)

	argv, err := buildArgv(t.Context(), t.TempDir(), Opts{
		Profile:   ProfileNetwork,
		RemoteURL: "https://example.com/repo.git",
	}, []string{"ls-remote", "https://example.com/repo.git"})
	testutil.FailErr(t, "buildArgv", err)
	// The reset entry must be the only helper.
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-c" && strings.HasPrefix(argv[i+1], "credential.helper=") &&
			argv[i+1] != "credential.helper=" {
			t.Fatalf("nil host config must not inject a credential helper; got %v", argv)
		}
	}

	dir := t.TempDir()
	initRepo(t, dir)
	out, code, err := Run(t.Context(), dir, []string{"status", "--porcelain"}, Opts{Profile: ProfileNetwork})
	if err != nil || code != 0 {
		t.Fatalf("status: err=%v code=%d out=%s", err, code, out)
	}
}

func TestIdentityFromEnv(t *testing.T) {
	useBundledGit(t)
	dir := t.TempDir()
	initRepo(t, dir)
	writeFile(t, dir, "a.txt", "hello\n")

	out, code, err := Run(t.Context(), dir, []string{"add", "a.txt"}, Opts{})
	if err != nil || code != 0 {
		t.Fatalf("add: err=%v code=%d out=%s", err, code, out)
	}
	out, code, err = Run(t.Context(), dir, []string{"commit", "-m", "init"}, Opts{
		Identity: &Identity{
			Name:      "Ada Lovelace",
			Email:     "ada@example.com",
			Timestamp: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC),
		},
	})
	if err != nil || code != 0 {
		t.Fatalf("commit without configured identity: err=%v code=%d out=%s", err, code, out)
	}
	out, code, err = Run(t.Context(), dir, []string{"log", "-1", "--format=%an <%ae>%n%aI%n%cI"}, Opts{})
	if err != nil || code != 0 {
		t.Fatalf("log: err=%v code=%d out=%s", err, code, out)
	}
	got := strings.TrimSpace(string(out))
	want := "Ada Lovelace <ada@example.com>\n2020-01-02T03:04:05Z\n2020-01-02T03:04:05Z"
	if got != want {
		t.Fatalf("identity = %q, want %q", got, want)
	}
}

func TestHooksDoNotRun(t *testing.T) {
	useBundledGit(t)
	dir := t.TempDir()
	initRepo(t, dir)

	sentinel := filepath.Join(dir, "hook-sentinel")
	hookBody := fmt.Sprintf("#!/bin/sh\necho ran >> %q\n", sentinel)
	writeExecutable(t, filepath.Join(dir, ".git", "hooks", "pre-commit"), hookBody)
	writeExecutable(t, filepath.Join(dir, ".git", "hooks", "post-checkout"), hookBody)

	writeFile(t, dir, "f.txt", "x\n")
	commitWithIdentity(t, dir, "c1")

	writeFile(t, dir, "f.txt", "y\n")
	commitWithIdentity(t, dir, "c2")
	out, code, err := Run(t.Context(), dir, []string{"rev-parse", "HEAD~1"}, Opts{})
	if err != nil || code != 0 {
		t.Fatalf("rev-parse: err=%v code=%d out=%s", err, code, out)
	}
	sha := strings.TrimSpace(string(out))
	out, code, err = Run(t.Context(), dir, []string{"checkout", sha}, Opts{})
	if err != nil || code != 0 {
		t.Fatalf("checkout: err=%v code=%d out=%s", err, code, out)
	}

	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		raw, _ := os.ReadFile(sentinel)
		t.Fatalf("hook sentinel appeared (%v): %s", err, raw)
	}
}

func TestRepoLocalHooksPathOverridden(t *testing.T) {
	useBundledGit(t)
	dir := t.TempDir()
	initRepo(t, dir)

	sentinel := filepath.Join(dir, "githooks-sentinel")
	hooksDir := filepath.Join(dir, ".githooks")
	writeExecutable(t, filepath.Join(hooksDir, "pre-commit"),
		fmt.Sprintf("#!/bin/sh\necho ran >> %q\n", sentinel))

	// Write the hostile value without invoking Git.
	cfgPath := filepath.Join(dir, ".git", "config")
	raw, err := os.ReadFile(cfgPath)
	testutil.FailErr(t, "read config", err)
	raw = append(raw, []byte("\n[core]\n\thooksPath = .githooks\n")...)
	testutil.FailErr(t, "write config", os.WriteFile(cfgPath, raw, 0o644))

	writeFile(t, dir, "f.txt", "x\n")
	commitWithIdentity(t, dir, "c1")

	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		raw, _ := os.ReadFile(sentinel)
		t.Fatalf("repo-local hooksPath hook ran: %s", raw)
	}
}

func TestPoisonedGlobalConfigInert(t *testing.T) {
	useBundledGit(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	poison := filepath.Join(home, ".gitconfig")
	testutil.FailErr(t, "write poisoned gitconfig", os.WriteFile(poison, []byte(`
[core]
	pager = false
[diff]
	external = /usr/bin/false
[filter "x"]
	clean = /usr/bin/false
[alias]
	st = "!echo poisoned-alias"
`), 0o644))

	clean := t.TempDir()
	poisoned := t.TempDir()
	for _, dir := range []string{clean, poisoned} {
		initRepo(t, dir)
		writeFile(t, dir, "a.txt", "same\n")
		commitWithIdentity(t, dir, "init")
	}

	ops := [][]string{
		{"status", "--porcelain", "-b"},
		{"log", "-1", "--format=%H%n%s"},
		{"diff", "HEAD"},
	}
	for _, args := range ops {
		a, codeA, errA := Run(t.Context(), clean, args, Opts{})
		b, codeB, errB := Run(t.Context(), poisoned, args, Opts{})
		if errA != nil || errB != nil || codeA != 0 || codeB != 0 {
			t.Fatalf("%v: clean(err=%v code=%d) poisoned(err=%v code=%d)", args, errA, codeA, errB, codeB)
		}
		if args[0] == "log" {
			// Commit hashes differ across repositories.
			sa := strings.SplitN(strings.TrimSpace(string(a)), "\n", 2)
			sb := strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)
			if len(sa) < 2 || len(sb) < 2 || sa[1] != sb[1] {
				t.Fatalf("log subjects differ: %q vs %q", a, b)
			}
			continue
		}
		if string(a) != string(b) {
			t.Fatalf("%v output differs under poisoned global:\nclean=%q\npoisoned=%q", args, a, b)
		}
	}
}

func TestNoConfigMutation(t *testing.T) {
	useBundledGit(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	global := filepath.Join(home, ".gitconfig")
	testutil.FailErr(t, "seed global", os.WriteFile(global, []byte("[user]\n\tname = Keep\n"), 0o644))

	dir := t.TempDir()
	initRepo(t, dir)
	cfg := filepath.Join(dir, ".git", "config")
	beforeRepo := fileSHA(t, cfg)
	beforeGlobal := fileSHA(t, global)

	writeFile(t, dir, "a.txt", "1\n")
	commitWithIdentity(t, dir, "c1")
	out, code, err := Run(t.Context(), dir, []string{"status"}, Opts{})
	if err != nil || code != 0 {
		t.Fatalf("status: err=%v code=%d out=%s", err, code, out)
	}
	out, code, err = Run(t.Context(), dir, []string{"checkout", "-b", "topic"}, Opts{})
	if err != nil || code != 0 {
		t.Fatalf("checkout: err=%v code=%d out=%s", err, code, out)
	}

	if got := fileSHA(t, cfg); got != beforeRepo {
		t.Fatalf(".git/config mutated: before=%s after=%s", beforeRepo, got)
	}
	if got := fileSHA(t, global); got != beforeGlobal {
		t.Fatalf("~/.gitconfig mutated: before=%s after=%s", beforeGlobal, got)
	}
}

func TestExtProtocolDenied(t *testing.T) {
	useBundledGit(t)
	parent := t.TempDir()
	dest := filepath.Join(parent, "clone-dest")
	out, code, err := Run(t.Context(), parent, []string{
		"clone", "ext::sh -c 'echo pwned >" + filepath.Join(parent, "pwned") + "'", dest,
	}, Opts{Profile: ProfileNetwork})
	if err != nil {
		return
	}
	if code == 0 {
		t.Fatalf("ext:: clone succeeded; out=%s", out)
	}
	if _, err := os.Stat(filepath.Join(parent, "pwned")); !os.IsNotExist(err) {
		t.Fatal("ext:: remote helper executed")
	}
}

func TestHostConfigInjectsHelper(t *testing.T) {
	useBundledGit(t)
	SetHostConfig(stubHostConfig{helper: "osxkeychain"})
	executablePath = func() (string, error) { return "/Applications/Painted Wolf Code.app/Contents/MacOS/pw", nil }
	t.Cleanup(func() { executablePath = os.Executable })

	argv, err := buildArgv(context.Background(), t.TempDir(), Opts{
		Profile:   ProfileNetwork,
		RemoteURL: "https://example.com/r.git",
	}, []string{"fetch"})
	testutil.FailErr(t, "buildArgv", err)
	found := false
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-c" && argv[i+1] == "credential.helper=!'/Applications/Painted Wolf Code.app/Contents/MacOS/pw' git-credential-osxkeychain" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected credential.helper injection in %v", argv)
	}
}

func TestBuildEnvRestoresResolvedSSHAgentSocket(t *testing.T) {
	emptyHooksDirFn = func() (string, error) { return t.TempDir(), nil }
	t.Cleanup(func() {
		emptyHooksDirFn = gitengine.EmptyHooksDir
		agentSocketResolver = defaultAgentSocket
	})
	agentSocketResolver = func(context.Context) string { return "/private/tmp/launchd/agent.sock" }
	env, err := buildEnv(t.Context(), "/bundle/bin/git", Opts{})
	testutil.FailErr(t, "buildEnv", err)
	found := false
	for _, entry := range env {
		if entry == "SSH_AUTH_SOCK=/private/tmp/launchd/agent.sock" {
			found = true
		}
	}
	if !found {
		t.Fatalf("SSH_AUTH_SOCK missing from %v", env)
	}
}

type stubHostConfig struct {
	helper string
	ssh    string
}

func (s stubHostConfig) HelperFor(context.Context, string) string { return s.helper }
func (s stubHostConfig) SSHCommand(context.Context) string        { return s.ssh }
