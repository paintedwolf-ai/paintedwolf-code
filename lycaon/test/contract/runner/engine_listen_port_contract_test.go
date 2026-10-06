package contract

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/osprocess"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const pythonHealthListener = `
import socket, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

class H(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b'{"status":"ok"}')
    def log_message(self, *args):
        pass

class Server(HTTPServer):
    def server_bind(self):
        self.socket.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.socket.bind(self.server_address)
        self.server_address = self.socket.getsockname()
        self.server_name = "127.0.0.1"
        self.server_port = self.server_address[1]

httpd = Server(("127.0.0.1", 0), H)
with open(sys.argv[1], "w", encoding="utf-8") as f:
    f.write(str(httpd.server_address[1]))
httpd.serve_forever()
`

const pythonMuteListener = `
import socket, sys, time
s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", 0))
s.listen(1)
open(sys.argv[1], "w", encoding="utf-8").write(str(s.getsockname()[1]))
while True:
    time.sleep(60)
`

func TestDenSidecarStartUsesEnsureEngineListenPort(t *testing.T) {
	t.Parallel()
	sidecar := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "scripts/den-dev-sidecar.sh")
	if !strings.Contains(sidecar, `ensure_engine_listen_port "${LYCAON_ADDR}" is_engine_pid`) {
		t.Fatal("den-dev-sidecar.sh must call ensure_engine_listen_port")
	}
	reclaimCall := regexp.MustCompile(`(?m)^\s*reclaim_listen_port\s`)
	if reclaimCall.MatchString(sidecar) {
		t.Fatal("den-dev-sidecar.sh must not call reclaim_listen_port")
	}

	fresh := taskBody(taskfile(t), "den:sidecar:fresh")
	if !strings.Contains(fresh, "den:sidecar:stop") {
		t.Fatal("den:sidecar:fresh must run den:sidecar:stop")
	}
	start := taskBody(taskfile(t), "den:sidecar")
	if strings.Contains(start, "den:sidecar:stop") {
		t.Fatal("den:sidecar must not run den:sidecar:stop")
	}
}

func TestEnsureEngineListenPortRefusesLiveHealth(t *testing.T) {
	t.Parallel()
	helper := startPythonListener(t, pythonHealthListener)
	waitHTTPHealth(t, helper.addr)

	output, err := runEnsureEngineListenPort(t, helper.addr, helper.pid)
	if err == nil {
		t.Fatalf("expected refuse for live /health, got success:\n%s", output)
	}
	if !strings.Contains(string(output), "held by a live engine") {
		t.Fatalf("refuse message missing:\n%s", output)
	}
	if !strings.Contains(string(output), "den:sidecar:stop") {
		t.Fatalf("refuse must mention den:sidecar:stop:\n%s", output)
	}
	assertPIDAlive(t, helper.pid)
}

func TestEnsureEngineListenPortReclaimsUnresponsiveListener(t *testing.T) {
	t.Parallel()
	helper := startPythonListener(t, pythonMuteListener)

	output, err := runEnsureEngineListenPort(t, helper.addr, helper.pid)
	if err != nil {
		t.Fatalf("expected reclaim of unresponsive listener: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "stopping unresponsive") {
		t.Fatalf("reclaim message missing:\n%s", output)
	}
	helper.waitReaped(t)
}

func TestEnsureEngineListenPortLeavesForeignListener(t *testing.T) {
	t.Parallel()
	helper := startPythonListener(t, pythonMuteListener)

	output, err := runEnsureEngineListenPort(t, helper.addr, -1)
	if err == nil {
		t.Fatalf("expected refuse for a foreign listener:\n%s", output)
	}
	if !strings.Contains(string(output), "in use by") {
		t.Fatalf("foreign-listener message missing:\n%s", output)
	}
	assertPIDAlive(t, helper.pid)
}

func TestEnsureEngineListenPortAllowsFreePort(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	contractcheck.FailErr(t, "reserve free port", err)
	addr := ln.Addr().String()
	contractcheck.FailErr(t, "release free port", ln.Close())

	output, err := runEnsureEngineListenPort(t, addr, 0)
	if err != nil {
		t.Fatalf("free port must succeed: %v\n%s", err, output)
	}
}

type pythonListener struct {
	pid    int
	addr   string
	cmd    *exec.Cmd
	reaped bool
}

func startPythonListener(t *testing.T, script string) *pythonListener {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required to bind a disposable listener")
	}
	portFile := filepath.Join(t.TempDir(), "port")
	cmd := exec.Command("python3", "-u", "-c", script, portFile)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	contractcheck.FailErr(t, "start python listener", cmd.Start())
	helper := &pythonListener{pid: cmd.Process.Pid, cmd: cmd}
	t.Cleanup(helper.cleanup)

	port := waitPortFile(t, portFile, &stderr)
	helper.addr = "127.0.0.1:" + port
	waitListen(t, port, helper.pid)
	return helper
}

func (l *pythonListener) cleanup() {
	if l == nil || l.cmd == nil || l.cmd.Process == nil || l.reaped {
		return
	}
	_ = l.cmd.Process.Kill()
	_ = l.cmd.Wait()
	l.reaped = true
}

func (l *pythonListener) waitReaped(t *testing.T) {
	t.Helper()
	if l.reaped {
		return
	}
	done := make(chan error, 1)
	go func() { done <- l.cmd.Wait() }()
	select {
	case <-done:
		l.reaped = true
	case <-time.After(3 * time.Second):
		t.Fatalf("unresponsive listener pid %d still running", l.pid)
	}
}

func waitPortFile(t *testing.T, path string, stderr *strings.Builder) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil {
			port := strings.TrimSpace(string(b))
			if port != "" {
				return port
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("listener never wrote %s\n%s", path, stderr.String())
	return ""
}

func waitListen(t *testing.T, port string, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	needle := strconv.Itoa(pid)
	for time.Now().Before(deadline) {
		out, err := exec.Command("lsof", "-ti", ":"+port, "-sTCP:LISTEN").Output()
		if err == nil && strings.Contains(string(out), needle) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d never listened on :%s", pid, port)
}

func waitHTTPHealth(t *testing.T, addr string) {
	t.Helper()
	url := "http://" + addr + "/health"
	client := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("GET %s never returned 200", url)
}

func runEnsureEngineListenPort(t *testing.T, addr string, pid int) ([]byte, error) {
	t.Helper()
	root := contractcheck.RepoRoot(t)
	lib := filepath.Join(root, "scripts", "listen-port.sh")
	script := `
set -euo pipefail
source "$1"
want="$2"
addr="$3"
is_ours() { [[ "$1" == "$want" ]]; }
ensure_engine_listen_port "$addr" is_ours "den:sidecar" "set LYCAON_ADDR"
`
	cmd := exec.Command("bash", "-c", script, "ensure-engine-listen", lib, strconv.Itoa(pid), addr)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+os.Getenv("PATH"))
	return cmd.CombinedOutput()
}

func assertPIDAlive(t *testing.T, pid int) {
	t.Helper()
	if !osprocess.Alive(pid) {
		t.Fatalf("live engine pid %d was killed", pid)
	}
}
