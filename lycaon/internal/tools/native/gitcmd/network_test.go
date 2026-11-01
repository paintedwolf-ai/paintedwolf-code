package gitcmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func serveSmartHTTP(bin, repoDir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if proto := r.Header.Get("Git-Protocol"); proto != "" {
			env = append(env, "GIT_PROTOCOL="+proto)
		}
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/info/refs"):
			svc := r.URL.Query().Get("service")
			sub := strings.TrimPrefix(svc, "git-")
			if sub != "upload-pack" && sub != "receive-pack" {
				http.Error(w, "unsupported service", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/x-"+svc+"-advertisement")
			pkt := "# service=" + svc + "\n"
			_, _ = fmt.Fprintf(w, "%04x%s0000", len(pkt)+4, pkt)
			cmd := exec.Command(bin, sub, "--stateless-rpc", "--advertise-refs", repoDir)
			cmd.Env = env
			cmd.Stdout = w
			_ = cmd.Run()
		case r.Method == http.MethodPost && (strings.HasSuffix(r.URL.Path, "/git-upload-pack") || strings.HasSuffix(r.URL.Path, "/git-receive-pack")):
			svc := filepath.Base(r.URL.Path)
			sub := strings.TrimPrefix(svc, "git-")
			w.Header().Set("Content-Type", "application/x-"+svc+"-result")
			cmd := exec.Command(bin, sub, "--stateless-rpc", repoDir)
			cmd.Env = env
			cmd.Stdin = r.Body
			cmd.Stdout = w
			_ = cmd.Run()
		default:
			http.NotFound(w, r)
		}
	})
}

func TestNetworkCommandsOverLocalFileRemote(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.git("reset", "--hard", "HEAD")
	h.git("clean", "-fd")

	bareParent, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "eval bare parent", err)
	bareDir := filepath.Join(bareParent, "origin.git")
	if out, stderr, status := h.direct(bareParent, "init", "--bare", "-q", "-b", "main", bareDir); status != 0 {
		t.Fatalf("init bare: %d %s %s", status, out, stderr)
	}
	fileURL := "file://" + filepath.ToSlash(bareDir)
	h.git("remote", "add", "origin", fileURL)

	if push := h.exec("git", "push", "-u", "origin", "main"); push.err != nil || push.result.Status != 0 || push.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("git push file://: %+v %v %s", push.result, push.err, push.stderr)
	}
	lsRemote := h.exec("git", "ls-remote", "--heads", "origin")
	if lsRemote.err != nil || lsRemote.result.Status != 0 || !strings.Contains(lsRemote.stdout, "refs/heads/main") {
		t.Fatalf("git ls-remote: %+v %q %v", lsRemote.result, lsRemote.stdout, lsRemote.err)
	}
	if clone := h.exec("git", "clone", "-b", "main", fileURL, "cloned-file"); clone.err != nil || clone.result.Status != 0 {
		t.Fatalf("git clone file://: %+v %v %s", clone.result, clone.err, clone.stderr)
	}
	clonedDir := filepath.Join(h.root, "cloned-file")
	if _, err := os.Stat(filepath.Join(clonedDir, "a.txt")); err != nil {
		t.Fatalf("cloned file missing: %v", err)
	}

	h.write("cloned-file/remote-change.txt", "from clone\n")
	if r := h.exec("git", "-C", "cloned-file", "add", "remote-change.txt"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("add in clone: %+v %v", r.result, r.err)
	}
	if r := h.exec("git", "-C", "cloned-file", "commit", "-m", "remote advance"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("commit in clone: %+v %v %s", r.result, r.err, r.stderr)
	}
	if r := h.exec("git", "-C", "cloned-file", "push", "origin", "main"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("push from clone: %+v %v %s", r.result, r.err, r.stderr)
	}
	testutil.FailErr(t, "remove clone before pull", os.RemoveAll(clonedDir))

	if fetch := h.exec("git", "fetch", "origin"); fetch.err != nil || fetch.result.Status != 0 {
		t.Fatalf("git fetch: %+v %v %s", fetch.result, fetch.err, fetch.stderr)
	}
	if pull := h.exec("git", "pull", "--ff-only", "origin", "main"); pull.err != nil || pull.result.Status != 0 {
		t.Fatalf("git pull --ff-only: %+v %v %s", pull.result, pull.err, pull.stderr)
	}
	if _, err := os.Stat(filepath.Join(h.root, "remote-change.txt")); err != nil {
		t.Fatalf("remote-change.txt missing after pull: %v", err)
	}
}

// Serial: mutates process-wide egress posture and resolver.
func TestNetworkCommandsOverSmartHTTPAndEgressGate(t *testing.T) {
	h := newHarness(t)
	h.git("reset", "--hard", "HEAD")
	h.git("clean", "-fd")

	bareParent, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "eval bare parent", err)
	bareDir := filepath.Join(bareParent, "remote.git")
	if out, errOut, code := h.direct(bareParent, "init", "--bare", "-q", "-b", "main", bareDir); code != 0 {
		t.Fatalf("init bare: %d %s %s", code, out, errOut)
	}
	if out, errOut, code := h.direct(bareDir, "config", "http.receivepack", "true"); code != 0 {
		t.Fatalf("config http.receivepack: %d %s %s", code, out, errOut)
	}

	server := httptest.NewServer(serveSmartHTTP(h.bin, bareDir))
	defer server.Close()

	remoteURL := server.URL + "/remote.git"
	h.git("remote", "add", "origin", remoteURL)

	confine.SetEgressPosture(confine.PostureAsk)
	t.Cleanup(func() {
		confine.SetEgressPosture(confine.PostureObserve)
		confine.SetEgressResolver(nil)
	})
	confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, _ egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
		return false
	})

	for _, argv := range [][]string{
		{"git", "fetch", "origin"},
		{"git", "pull", "origin", "main"},
		{"git", "push", "origin", "main"},
		{"git", "ls-remote", "origin"},
		{"git", "clone", remoteURL, "denied-clone"},
	} {
		h.exec(argv...).refused(t, "denied "+strings.Join(argv, " "), "GIT_REMOTE_HOST_DENIED")
	}

	confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, ep egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
		return ep.Host == "127.0.0.1" || ep.Host == "localhost"
	})

	if push := h.exec("git", "push", "-u", "origin", "main"); push.err != nil || push.result.Status != 0 {
		t.Fatalf("http push: %+v %v %s", push.result, push.err, push.stderr)
	}
	ls := h.exec("git", "ls-remote", "origin")
	if ls.err != nil || ls.result.Status != 0 || !strings.Contains(ls.stdout, "refs/heads/main") {
		t.Fatalf("http ls-remote: %+v %q %v %s", ls.result, ls.stdout, ls.err, ls.stderr)
	}
	if clone := h.exec("git", "clone", remoteURL, "http-clone"); clone.err != nil || clone.result.Status != 0 {
		t.Fatalf("http clone: %+v %v %s", clone.result, clone.err, clone.stderr)
	}
	h.write("http-clone/http.txt", "via smart http\n")
	if r := h.exec("git", "-C", "http-clone", "add", "http.txt"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("add in http-clone: %+v %v", r.result, r.err)
	}
	if r := h.exec("git", "-C", "http-clone", "commit", "-m", "http commit"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("commit in http-clone: %+v %v", r.result, r.err)
	}
	if r := h.exec("git", "-C", "http-clone", "push", "origin", "main"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("push from http-clone: %+v %v %s", r.result, r.err, r.stderr)
	}
	testutil.FailErr(t, "remove http-clone before pull", os.RemoveAll(filepath.Join(h.root, "http-clone")))

	if fetch := h.exec("git", "fetch", "origin"); fetch.err != nil || fetch.result.Status != 0 {
		t.Fatalf("http fetch: %+v %v %s", fetch.result, fetch.err, fetch.stderr)
	}
	if pull := h.exec("git", "pull", "--rebase", "origin", "main"); pull.err != nil || pull.result.Status != 0 {
		t.Fatalf("http pull --rebase: %+v %v %s", pull.result, pull.err, pull.stderr)
	}
	if _, err := os.Stat(filepath.Join(h.root, "http.txt")); err != nil {
		t.Fatalf("http.txt missing after http pull: %v", err)
	}

	h.exec("git", "clone", "ext::sh -c echo", "bad").refused(t, "ext transport", "COMMAND_OPERAND_INVALID")
}

type sshTestHostConfig struct {
	sshCmd string
}

func (c sshTestHostConfig) HelperFor(context.Context, string) string { return "" }
func (c sshTestHostConfig) SSHCommand(context.Context) string        { return c.sshCmd }

func (h *harness) execWithEnv(env []string, words ...string) run {
	h.t.Helper()
	inv := toolcontract.OperationInvocation{
		Kind:          toolcontract.ProgramChildInvocation,
		Identity:      toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "n1", ExecutionID: uuid.NewString()},
		ActorID:       "actor",
		SessionID:     "s1",
		Scope:         toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: "p1", RootID: "r1"},
		SourceUnitID:  "program",
		Span:          toolcontract.SourceSpan{Line: 1, Column: 1},
		CatalogDigest: toolcontract.LanguageCatalogDigest(),
	}
	argv := make(commandprogram.Fields, len(words))
	for i, word := range words {
		argv[i] = commandprogram.Field{Value: word, Span: inv.Span}
	}
	statement := commandsurface.Statement{SyntaxLocation: commandsurface.SyntaxLocation{ID: commandsurface.NodeID(inv.Identity.NodeID), Span: inv.Span}}
	request := commandprogram.OperationRequest{Invocation: inv, OperationID: "git", Statement: statement, Argv: argv, Environment: env}
	prepared, err := h.owners.PrepareGit(h.paths, h.filesystem, h.nested, request)
	if err != nil {
		return run{err: err, result: toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, EffectCertainty: toolcontract.NoEffect}}
	}
	var stdout, stderr bytes.Buffer
	result, err := prepared.InvokeOwner(h.t.Context(), h.tc, commandprogram.Streams{Input: io.NopCloser(strings.NewReader("")), Output: &stdout, Error: &stderr})
	return run{stdout: stdout.String(), stderr: stderr.String(), result: result, err: err}
}

func configureSSHTransport(t *testing.T, h *harness, bareParent, bareDir string) (sockPath, argvLog, envLog string) {
	t.Helper()
	sockDir, err := os.MkdirTemp(os.TempDir(), "pws-") //nolint:usetesting // SSH_AUTH_SOCK needs a short path; registered cleanup removes this owned directory.
	testutil.FailErr(t, "mkdir socket dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sockPath = filepath.Join(sockDir, "agent.sock")
	ln, err := net.Listen("unix", sockPath)
	testutil.FailErr(t, "listen unix socket", err)
	t.Cleanup(func() { _ = ln.Close() })

	t.Setenv("SSH_AUTH_SOCK", sockPath)
	t.Setenv("GIT_SSH_COMMAND", "/bin/false")
	t.Setenv("SSH_ASKPASS", "/bin/false")

	cfgPath := filepath.Join(h.root, ".git", "config")
	origCfg, err := os.ReadFile(cfgPath)
	testutil.FailErr(t, "read .git/config", err)
	testutil.FailErr(t, "plant repo-local sshCommand", os.WriteFile(cfgPath, append(origCfg, []byte("\n[core]\n\tsshCommand = /bin/false\n")...), 0o644))

	argvLog = filepath.Join(bareParent, "ssh-argv.log")
	envLog = filepath.Join(bareParent, "ssh-env.log")
	sshShim := filepath.Join(bareParent, "ssh")
	shimBody := fmt.Sprintf(`#!/bin/sh
case "$*" in
  *-G*) exit 0 ;;
esac
printf '%%s\n' "$*" >> %q
printf 'SSH_AUTH_SOCK=%%s\nSSH_ASKPASS=%%s\nGIT_TERMINAL_PROMPT=%%s\nGIT_SSH_COMMAND=%%s\n' "$SSH_AUTH_SOCK" "$SSH_ASKPASS" "$GIT_TERMINAL_PROMPT" "$GIT_SSH_COMMAND" > %q
case "$*" in
  *receive-pack*) exec %q receive-pack %q ;;
  *upload-pack*) exec %q upload-pack %q ;;
  *) echo "unexpected ssh argv: $*" >&2; exit 1 ;;
esac
`, argvLog, envLog, h.bin, bareDir, h.bin, bareDir)
	testutil.FailErr(t, "write ssh shim", os.WriteFile(sshShim, []byte(shimBody), 0o755))

	gitexec.SetHostConfig(sshTestHostConfig{sshCmd: sshShim})
	t.Cleanup(func() { gitexec.SetHostConfig(nil) })

	return sockPath, argvLog, envLog
}

// Serial: mutates process-wide SSH environment, host config, and egress gate.
func TestNetworkCommandsOverSSHRemotesAndEgressGate(t *testing.T) {
	h := newHarness(t)
	h.git("reset", "--hard", "HEAD")
	h.git("clean", "-fd")

	bareParent, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "eval bare parent", err)
	bareDir := filepath.Join(bareParent, "remote.git")
	if out, errOut, code := h.direct(bareParent, "init", "--bare", "-q", "-b", "main", bareDir); code != 0 {
		t.Fatalf("init bare: %d %s %s", code, out, errOut)
	}

	sockPath, argvLog, envLog := configureSSHTransport(t, h, bareParent, bareDir)

	scpRemote := "git@ssh.example.test:org/remote.git"
	urlRemote := "ssh://git@ssh-alt.example.test:2222/org/remote.git"
	h.git("remote", "add", "origin", scpRemote)
	h.git("remote", "add", "sshurl", urlRemote)

	for _, envEntry := range []string{
		"GIT_SSH_COMMAND=/bin/false",
		"GIT_SSH=/bin/false",
		"GIT_ASKPASS=/bin/false",
		"SSH_AUTH_SOCK=/tmp/override.sock",
		"SSH_ASKPASS=/bin/false",
		"SSH_ASKPASS_REQUIRE=force",
		"LYCAON_SSH_KNOWN_HOSTS=/tmp/known_hosts",
		"LYCAON_SOCKS_PROXY=127.0.0.1:1080",
	} {
		h.execWithEnv([]string{envEntry}, "git", "fetch", "origin").refused(t, "env "+envEntry, "COMMAND_OPERAND_INVALID")
	}

	confine.SetEgressPosture(confine.PostureAsk)
	t.Cleanup(func() {
		confine.SetEgressPosture(confine.PostureObserve)
		confine.SetEgressResolver(nil)
	})
	confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, _ egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
		return false
	})

	for _, tc := range []struct {
		argv []string
		host string
	}{
		{[]string{"git", "fetch", "origin"}, "ssh.example.test"},
		{[]string{"git", "fetch", "--all"}, "ssh.example.test"},
		{[]string{"git", "fetch"}, "ssh.example.test"},
		{[]string{"git", "fetch", "sshurl"}, "ssh-alt.example.test"},
		{[]string{"git", "pull", "origin", "main"}, "ssh.example.test"},
		{[]string{"git", "pull", "sshurl", "main"}, "ssh-alt.example.test"},
		{[]string{"git", "push", "origin", "main"}, "ssh.example.test"},
		{[]string{"git", "push", "sshurl", "main"}, "ssh-alt.example.test"},
		{[]string{"git", "ls-remote", "origin"}, "ssh.example.test"},
		{[]string{"git", "ls-remote", "sshurl"}, "ssh-alt.example.test"},
		{[]string{"git", "ls-remote", "--", scpRemote}, "ssh.example.test"},
		{[]string{"git", "clone", scpRemote, "denied-scp"}, "ssh.example.test"},
		{[]string{"git", "clone", urlRemote, "denied-url"}, "ssh-alt.example.test"},
		{[]string{"git", "clone", "git@[::1]:org/remote.git", "denied-v6"}, "::1"},
	} {
		r := h.exec(tc.argv...)
		r.refused(t, "denied "+strings.Join(tc.argv, " "), "GIT_REMOTE_HOST_DENIED")
		if !strings.Contains(r.stderr, "fatal: ") || !strings.Contains(r.stderr, tc.host) || !strings.Contains(r.stderr, "[GIT_REMOTE_HOST_DENIED]") {
			t.Fatalf("%v stderr = %q, want shell-voice refusal naming %s and GIT_REMOTE_HOST_DENIED", tc.argv, r.stderr, tc.host)
		}
	}
	if _, err := os.Stat(argvLog); err == nil {
		t.Fatalf("ssh transport ran while egress gate denied remote host")
	}

	confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, ep egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
		return ep.Host == "ssh.example.test" || ep.Host == "ssh-alt.example.test"
	})

	if push := h.exec("git", "push", "-u", "origin", "main"); push.err != nil || push.result.Status != 0 || push.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("ssh push: %+v %v %s", push.result, push.err, push.stderr)
	}
	for _, remote := range []string{"origin", "sshurl"} {
		ls := h.exec("git", "ls-remote", "--heads", remote)
		if ls.err != nil || ls.result.Status != 0 || !strings.Contains(ls.stdout, "refs/heads/main") {
			t.Fatalf("ssh ls-remote %s: %+v %q %v %s", remote, ls.result, ls.stdout, ls.err, ls.stderr)
		}
	}
	if got := defaultCloneDirName(urlRemote); got != "remote" {
		t.Fatalf("defaultCloneDirName(%q) = %q, want remote", urlRemote, got)
	}
	if cloneSCP := h.exec("git", "clone", scpRemote); cloneSCP.err != nil || cloneSCP.result.Status != 0 {
		t.Fatalf("ssh clone scp default dir: %+v %v %s", cloneSCP.result, cloneSCP.err, cloneSCP.stderr)
	}
	if _, err := os.Stat(filepath.Join(h.root, "remote", "a.txt")); err != nil {
		t.Fatalf("scp cloned file missing: %v", err)
	}
	if cloneURL := h.exec("git", "clone", urlRemote, "ssh-url-clone"); cloneURL.err != nil || cloneURL.result.Status != 0 {
		t.Fatalf("ssh clone url: %+v %v %s", cloneURL.result, cloneURL.err, cloneURL.stderr)
	}

	h.write("ssh-url-clone/ssh.txt", "via ssh transport\n")
	if r := h.exec("git", "-C", "ssh-url-clone", "add", "ssh.txt"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("add in ssh-url-clone: %+v %v", r.result, r.err)
	}
	if r := h.exec("git", "-C", "ssh-url-clone", "commit", "-m", "ssh commit"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("commit in ssh-url-clone: %+v %v %s", r.result, r.err, r.stderr)
	}
	if r := h.exec("git", "-C", "ssh-url-clone", "push", "origin", "main"); r.err != nil || r.result.Status != 0 {
		t.Fatalf("push from ssh-url-clone: %+v %v %s", r.result, r.err, r.stderr)
	}
	testutil.FailErr(t, "remove clones before pull", os.RemoveAll(filepath.Join(h.root, "remote")))
	testutil.FailErr(t, "remove url clone before pull", os.RemoveAll(filepath.Join(h.root, "ssh-url-clone")))

	if fetch := h.exec("git", "fetch", "origin"); fetch.err != nil || fetch.result.Status != 0 {
		t.Fatalf("ssh fetch: %+v %v %s", fetch.result, fetch.err, fetch.stderr)
	}
	if pull := h.exec("git", "pull", "--ff-only", "sshurl", "main"); pull.err != nil || pull.result.Status != 0 {
		t.Fatalf("ssh pull --ff-only: %+v %v %s", pull.result, pull.err, pull.stderr)
	}
	if _, err := os.Stat(filepath.Join(h.root, "ssh.txt")); err != nil {
		t.Fatalf("ssh.txt missing after ssh pull: %v", err)
	}

	envBytes, err := os.ReadFile(envLog)
	testutil.FailErr(t, "read ssh env log", err)
	envText := string(envBytes)
	for _, want := range []string{
		"SSH_AUTH_SOCK=" + sockPath,
		"SSH_ASKPASS=",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_SSH_COMMAND=",
	} {
		if !strings.Contains(envText, want+"\n") {
			t.Fatalf("ssh transport env missing %q:\n%s", want, envText)
		}
	}
	argvBytes, err := os.ReadFile(argvLog)
	testutil.FailErr(t, "read ssh argv log", err)
	argvText := string(argvBytes)
	for _, want := range []string{"git@ssh.example.test", "-p 2222", "git@ssh-alt.example.test", "receive-pack", "upload-pack"} {
		if !strings.Contains(argvText, want) {
			t.Fatalf("ssh transport argv missing %q:\n%s", want, argvText)
		}
	}
}
