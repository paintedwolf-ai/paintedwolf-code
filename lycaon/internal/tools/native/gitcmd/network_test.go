package gitcmd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
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
