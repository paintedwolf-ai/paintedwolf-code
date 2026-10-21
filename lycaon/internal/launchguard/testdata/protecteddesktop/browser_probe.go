//go:build linux

package main

import (
	"bytes"
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/osprocess"
)

const fakeHeadlessBrowserPy = `#!/usr/bin/env python3
import base64, hashlib, os, signal, socket, sys, time
if os.fork() == 0:
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    time.sleep(60)
    os._exit(0)
srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
srv.bind(("127.0.0.1", 0))
srv.listen(4)
port = srv.getsockname()[1]
sys.stderr.write(f"DevTools listening on ws://127.0.0.1:{port}/devtools/browser/1\n")
sys.stderr.flush()
conn, _ = srv.accept()
req = conn.recv(4096).decode("utf-8", "ignore")
key = ""
for line in req.split("\r\n"):
    if line.lower().startswith("sec-websocket-key:"):
        key = line.split(":", 1)[1].strip()
accept = base64.b64encode(hashlib.sha1((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest()).decode()
conn.sendall(f"HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: {accept}\r\n\r\n".encode())
time.sleep(60)
`

func runBrowserEngineVictim(cacheDir, fakeChrome, readyFile string) int {
	_ = os.Setenv(exec.LaunchBrokerFDEnv, "3")
	_ = os.Setenv(browserengine.EnvBrowserBin, fakeChrome)
	confine.EnableAutoConfine()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b, _, err := browser.LaunchHeadless(ctx, browser.LaunchOptions{
		CacheDir:  cacheDir,
		DisableJS: true,
	})
	if err != nil || b == nil {
		if err != nil {
			_, _ = os.Stderr.WriteString(err.Error())
		}
		return 1
	}
	if err := os.WriteFile(readyFile, []byte("ready"), 0o600); err != nil {
		return 1
	}
	time.Sleep(60 * time.Second)
	return 0
}

// browserEngineLossProbe starts a child engine inside the protected namespace
// that launches a confined headless browser through the broker, SIGKILLs that
// engine, and requires the brokered browser tree outside the namespace to die.
func browserEngineLossProbe(scratch string) (bool, string) {
	cacheDir := filepath.Join(scratch, "browser-cache")
	binDir := filepath.Join(cacheDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return false, err.Error()
	}
	if err := os.WriteFile(filepath.Join(binDir, "icudtl.dat"), []byte("icu"), 0o644); err != nil {
		return false, err.Error()
	}
	fakeChrome := filepath.Join(binDir, "chrome-headless-shell")
	if err := os.WriteFile(fakeChrome, []byte(fakeHeadlessBrowserPy), 0o755); err != nil {
		return false, err.Error()
	}
	self, err := os.Readlink("/proc/self/exe")
	if err != nil {
		return false, err.Error()
	}
	readyFile := filepath.Join(cacheDir, "ready")
	var stderr bytes.Buffer
	victim := osexec.Command(self, "--browser-engine-victim", cacheDir, fakeChrome, readyFile)
	victim.Env = append(os.Environ(), exec.LaunchBrokerFDEnv+"=3")
	victim.ExtraFiles = []*os.File{os.NewFile(6, "broker")}
	victim.Stderr = &stderr
	if err := victim.Start(); err != nil {
		return false, err.Error()
	}
	defer func() {
		_ = victim.Process.Kill()
		_ = victim.Wait()
	}()

	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(readyFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return false, "browser victim never ready: " + stderr.String()
		}
		time.Sleep(20 * time.Millisecond)
	}

	pids := procPIDsContaining(cacheDir)
	if len(pids) < 2 {
		return false, "expected brokered browser and child helper in /proc, got " + strconv.Itoa(len(pids))
	}

	if err := unix.Kill(victim.Process.Pid, unix.SIGKILL); err != nil {
		return false, err.Error()
	}
	_ = victim.Wait()

	killDeadline := time.Now().Add(10 * time.Second)
	for _, pid := range pids {
		for osprocess.Alive(pid) {
			if time.Now().After(killDeadline) {
				return false, "brokered browser process " + strconv.Itoa(pid) + " survived SIGKILLed protected engine"
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if remaining := procPIDsContaining(cacheDir); len(remaining) != 0 {
		return false, "brokered browser processes survived SIGKILLed protected engine"
	}
	return true, ""
}

func procPIDsContaining(token string) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 {
			continue
		}
		raw, err := os.ReadFile("/proc/" + entry.Name() + "/cmdline")
		if err == nil && strings.Contains(string(raw), token) && osprocess.Alive(pid) {
			pids = append(pids, pid)
		}
	}
	return pids
}
