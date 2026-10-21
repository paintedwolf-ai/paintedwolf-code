//go:build linux

package launchguard

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/vaultidentity"
)

func (l *runningLaunch) vaultRequest(t *testing.T, socket string, req vaultidentity.Request) vaultidentity.Response {
	t.Helper()
	payload, err := json.Marshal(struct {
		Socket  string                `json:"socket"`
		Request vaultidentity.Request `json:"request"`
	}{Socket: socket, Request: req})
	testutil.FailErr(t, "encode vault command", err)
	_, err = l.stdin.Write(append(payload, '\n'))
	testutil.FailErr(t, "write vault command", err)
	line, err := l.reader.ReadString('\n')
	testutil.FailErr(t, "read vault response", err)
	var resp vaultidentity.Response
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("decode vault response %q: %v", line, err)
	}
	return resp
}

func startVaultIdentityServer(t *testing.T, f launchFixture) string {
	t.Helper()
	build := t.TempDir()
	vaultBin := filepath.Join(build, "pw-vault-identity")
	goBuild(t, vaultBin, "github.com/lycaon/lycaon/cmd/pw-vault-identity", "")
	testutil.FailErr(t, "chmod vault binary", os.Chmod(vaultBin, 0o755))
	testutil.FailErr(t, "chmod vault build dir", os.Chmod(build, 0o755))
	args := []string{
		"-n", "env",
		fixtureModeEnv + "=vault-server",
		fixtureDirEnv + "=" + f.dir,
		fixtureVaultBinEnv + "=" + vaultBin,
		fixtureUIDEnv + "=" + strconv.Itoa(os.Getuid()),
		fixtureGIDEnv + "=" + strconv.Itoa(os.Getgid()),
		os.Args[0], "-test.run=^TestProtectedLaunchFixture$",
	}
	cmd := exec.Command("sudo", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	testutil.FailErr(t, "start vault-identity server", cmd.Start())
	t.Cleanup(func() {
		if raw, err := os.ReadFile(filepath.Join(f.dir, "vault.pid")); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 1 {
				_ = syscall.Kill(pid, syscall.SIGTERM)
			}
		}
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
		}
	})
	socketPath := filepath.Join(f.dir, "run", "vault.sock")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			return socketPath
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("vault-identity socket never appeared at %s; stderr: %s", socketPath, stderr.String())
	return ""
}

func TestProtectedLaunchUpgradePreservesVaultIdentityCustody(t *testing.T) {
	f := installLaunchFixture(t)
	socketPath := startVaultIdentityServer(t, f)
	env := []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	const account = "age-x25519-identity-v1:0a"
	secret := []byte("AGE-SECRET-KEY-1UPGRADEWHILERUNNING")

	// Launch session A on generation A (1.0.0) and create a vault identity.
	launchA := startLaunch(t, f, env)
	created := launchA.vaultRequest(t, socketPath, vaultidentity.Request{
		Op:      vaultidentity.OpCreate,
		Account: account,
		Value:   secret,
	})
	if created.Status != vaultidentity.StatusOK {
		t.Fatalf("create on generation A = %+v", created)
	}

	// Upgrade in place to generation B (1.0.1) while session A stays running:
	// launch.json and install/manifest.json now point to generation B.
	digestA := f.digest
	testutil.FailErr(t, "upgrade fixture to generation B", sudoFixture(t, "upgrade", f.dir, fixtureBinsEnv+"="+strings.Join(f.binaries, ":")))
	rawDigestB, err := os.ReadFile(filepath.Join(f.dir, "digest"))
	testutil.FailErr(t, "read generation B digest", err)
	digestB := string(rawDigestB)
	if digestB == digestA {
		t.Fatalf("upgrade digest did not change: %s", digestB)
	}

	// Session A still holds its shared lifetime lock on generation A and its
	// relaunched engine child inside A's namespace keeps vault custody.
	if err := sudoFixture(t, "lock-free", f.dir, fixtureHash+"="+digestA); err == nil {
		t.Fatal("generation A lock was free while session A still ran")
	}
	recheckedA := launchA.vaultRequest(t, socketPath, vaultidentity.Request{
		Op:      vaultidentity.OpRead,
		Account: account,
	})
	if recheckedA.Status != vaultidentity.StatusOK || string(recheckedA.Value) != string(secret) {
		t.Fatalf("read from running session A after upgrade to B = %+v, want %q", recheckedA, secret)
	}

	// A new launch starts on generation B and verifies against generation B's manifest.
	launchB := startLaunch(t, f, env)
	fromB := launchB.vaultRequest(t, socketPath, vaultidentity.Request{
		Op:      vaultidentity.OpRead,
		Account: account,
	})
	if fromB.Status != vaultidentity.StatusOK || string(fromB.Value) != string(secret) {
		t.Fatalf("read from new session B on generation B = %+v, want %q", fromB, secret)
	}

	// Closing session A releases generation A's lifetime lock while session B continues.
	testutil.FailErr(t, "close session A", launchA.stdin.Close())
	testutil.FailErr(t, "wait session A", launchA.cmd.Wait())
	testutil.FailErr(t, "generation A lock released after session A exit", sudoFixture(t, "lock-free", f.dir, fixtureHash+"="+digestA))
}
