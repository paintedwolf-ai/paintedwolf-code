//go:build linux

package launchguard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"github.com/lycaon/lycaon/internal/testutil"
)

type testVaultRequest struct {
	Op      string `json:"op"`
	Account string `json:"account,omitempty"`
	Value   []byte `json:"value,omitempty"`
}

type testVaultResponse struct {
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Value   []byte `json:"value,omitempty"`
}

func (l *runningLaunch) vaultRequest(t *testing.T, socket string, req testVaultRequest) testVaultResponse {
	t.Helper()
	payload, err := json.Marshal(struct {
		Socket  string           `json:"socket"`
		Request testVaultRequest `json:"request"`
	}{Socket: socket, Request: req})
	testutil.FailErr(t, "encode vault command", err)
	_, err = l.stdin.Write(append(payload, '\n'))
	testutil.FailErr(t, "write vault command", err)
	line, err := l.reader.ReadString('\n')
	testutil.FailErr(t, "read vault response", err)
	var resp testVaultResponse
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
	created := launchA.vaultRequest(t, socketPath, testVaultRequest{
		Op:      "create",
		Account: account,
		Value:   secret,
	})
	if created.Status != "ok" {
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
	recheckedA := launchA.vaultRequest(t, socketPath, testVaultRequest{
		Op:      "read",
		Account: account,
	})
	if recheckedA.Status != "ok" || string(recheckedA.Value) != string(secret) {
		t.Fatalf("read from running session A after upgrade to B = %+v, want %q", recheckedA, secret)
	}

	// A new launch starts on generation B and verifies against generation B's manifest.
	launchB := startLaunch(t, f, env)
	fromB := launchB.vaultRequest(t, socketPath, testVaultRequest{
		Op:      "read",
		Account: account,
	})
	if fromB.Status != "ok" || string(fromB.Value) != string(secret) {
		t.Fatalf("read from new session B on generation B = %+v, want %q", fromB, secret)
	}

	// Closing session A releases generation A's lifetime lock while session B continues.
	testutil.FailErr(t, "close session A", launchA.stdin.Close())
	if err := launchA.wait(); err != nil {
		t.Fatalf("wait session A: %v; bootstrap stderr: %s", err, launchA.stderr.String())
	}
	testutil.FailErr(t, "generation A lock released after session A exit", sudoFixture(t, "lock-free", f.dir, fixtureHash+"="+digestA))
}

func TestProtectedEntryLocksOSThreadAndSealsEveryThreadUnderContention(t *testing.T) {
	f := installLaunchFixture(t)
	err := sudoFixture(
		t,
		"seal-entry-parent",
		f.dir,
		fixtureUIDEnv+"="+strconv.Itoa(os.Getuid()),
		fixtureGIDEnv+"="+strconv.Itoa(os.Getgid()),
	)
	testutil.FailErr(t, "seal entry under scheduler contention", err)
}

func runSealEntryParent(dir, uidStr, gidStr string) error {
	uid, err := strconv.Atoi(uidStr)
	if err != nil || uid <= 0 {
		return fmt.Errorf("invalid uid %q", uidStr)
	}
	gid, err := strconv.Atoi(gidStr)
	if err != nil || gid < 0 {
		return fmt.Errorf("invalid gid %q", gidStr)
	}
	origin, err := os.Open("/proc/self/ns/user")
	if err != nil {
		return err
	}
	defer func() { _ = origin.Close() }()
	attributes, err := EntryAttributes(Identity{UID: uint32(uid), GID: uint32(gid), Groups: []uint32{uint32(gid)}})
	if err != nil {
		return err
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestProtectedLaunchFixture$")
	cmd.Env = []string{
		fixtureModeEnv + "=seal-entry-child",
		fixtureDirEnv + "=" + dir,
		"PATH=/usr/bin:/bin",
	}
	cmd.ExtraFiles = []*os.File{origin}
	cmd.SysProcAttr = attributes
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("seal-entry-child: %w: %s", err, out)
	}
	return nil
}

func runSealEntryChild(dir string) error {
	runtime.GOMAXPROCS(4)
	const workerCount = 4
	ready := make(chan int, workerCount)
	checkExec := make(chan struct{})
	execResults := make(chan error, workerCount)
	for range workerCount {
		go func() {
			runtime.LockOSThread()
			ready <- unix.Gettid()
			<-checkExec
			_, err := syscall.ForkExec("/bin/true", []string{"/bin/true"}, &syscall.ProcAttr{Files: []uintptr{0, 1, 2}})
			if !errors.Is(err, unix.EACCES) {
				execResults <- fmt.Errorf("worker tid %d ForkExec(/bin/true) = %w, want EACCES", unix.Gettid(), err)
				return
			}
			execResults <- nil
		}()
	}
	workerTIDs := make([]int, 0, workerCount)
	for range workerCount {
		workerTIDs = append(workerTIDs, <-ready)
	}
	stopContenders := make(chan struct{})
	defer close(stopContenders)
	for range workerCount {
		go func() {
			for {
				select {
				case <-stopContenders:
					return
				default:
					runtime.Gosched()
					_, _ = os.Stat("/proc/self")
				}
			}
		}()
	}

	runtime.LockOSThread()
	tidBefore := unix.Gettid()
	if slices.Contains(workerTIDs, tidBefore) {
		return fmt.Errorf("entry tid %d collided with locked worker %v", tidBefore, workerTIDs)
	}
	origin := os.NewFile(3, "original-user-namespace")
	defer func() { _ = origin.Close() }()
	if err := SealEntry(origin); err != nil {
		return fmt.Errorf("SealEntry: %w", err)
	}
	groups, err := unix.Getgroups()
	if err != nil {
		return err
	}
	threads, err := verifyProcessThreadsStatus(os.Getpid(), os.Getuid(), os.Getgid(), groups, "1")
	if err != nil {
		return fmt.Errorf("entry thread status after SealEntry: %w", err)
	}
	if threads < workerCount+1 {
		return fmt.Errorf("entry checked %d threads, want at least %d", threads, workerCount+1)
	}

	rawPolicy, err := os.ReadFile(filepath.Join(dir, "launch.json"))
	if err != nil {
		return err
	}
	policy, err := decodePolicy(rawPolicy)
	if err != nil {
		return err
	}
	rawManifest, err := os.ReadFile(filepath.Join(policy.GenerationRoot, "manifest.json"))
	if err != nil {
		return err
	}
	manifest, err := pkgmanifest.Read(bytes.NewReader(rawManifest))
	if err != nil {
		return err
	}
	desktopSpec, ok := manifest.Lookup(filepath.Join(policy.LogicalInstallRoot, "bin/painted-wolf-code"))
	if !ok {
		return errors.New("desktop executable missing from fixture manifest")
	}
	image, err := os.Open(filepath.Join(policy.GenerationRoot, "bin/painted-wolf-code"))
	if err != nil {
		return err
	}
	defer func() { _ = image.Close() }()
	if err := pkgmanifest.VerifyOpened(image, desktopSpec); err != nil {
		return fmt.Errorf("VerifyOpened: %w", err)
	}
	if tidAfterVerify := unix.Gettid(); tidAfterVerify != tidBefore {
		return fmt.Errorf("entry migrated from tid %d to %d during VerifyOpened", tidBefore, tidAfterVerify)
	}
	if err := restrictExecution(policy, image); err != nil {
		return fmt.Errorf("restrictExecution: %w", err)
	}
	if tidAfterRestrict := unix.Gettid(); tidAfterRestrict != tidBefore {
		return fmt.Errorf("entry migrated from tid %d to %d during restrictExecution", tidBefore, tidAfterRestrict)
	}
	close(checkExec)
	for range workerCount {
		if err := <-execResults; err != nil {
			return err
		}
	}
	return nil
}

// verifyProcessThreadsStatus requires every task of pid to carry exactly the
// given identity on each credential, the given supplementary groups, no
// capabilities beyond the bounding set, and the given no_new_privs flag.
func verifyProcessThreadsStatus(pid, uid, gid int, groups []int, wantNoNewPrivs string) (int, error) {
	taskDir := fmt.Sprintf("/proc/%d/task", pid)
	entries, err := os.ReadDir(taskDir)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", taskDir, err)
	}
	if len(entries) == 0 {
		return 0, fmt.Errorf("no threads in %s", taskDir)
	}
	uidStr := strconv.Itoa(uid)
	gidStr := strconv.Itoa(gid)
	checked := 0
	for _, entry := range entries {
		statusPath := filepath.Join(taskDir, entry.Name(), "status")
		status, err := os.ReadFile(statusPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return checked, fmt.Errorf("read %s: %w", statusPath, err)
		}
		if err := verifyThreadStatus(statusPath, string(status), uidStr, gidStr, groups, wantNoNewPrivs); err != nil {
			return checked, err
		}
		checked++
	}
	if checked == 0 {
		return 0, fmt.Errorf("no readable thread status in %s", taskDir)
	}
	return checked, nil
}

func verifyThreadStatus(path, status, uid, gid string, groups []int, wantNoNewPrivs string) error {
	seen := map[string]bool{}
	for _, line := range strings.Split(status, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(value)
		switch key {
		case "Uid":
			seen[key] = true
			if len(fields) != 4 || slices.ContainsFunc(fields, func(f string) bool { return f != uid }) {
				return fmt.Errorf("%s uids %v, want %s", path, fields, uid)
			}
		case "Gid":
			seen[key] = true
			if len(fields) != 4 || slices.ContainsFunc(fields, func(f string) bool { return f != gid }) {
				return fmt.Errorf("%s gids %v, want %s", path, fields, gid)
			}
		case "Groups":
			seen[key] = true
			got := make([]int, 0, len(fields))
			for _, field := range fields {
				group, err := strconv.Atoi(field)
				if err != nil {
					return fmt.Errorf("%s groups %v: %w", path, fields, err)
				}
				got = append(got, group)
			}
			if !slices.Equal(got, groups) {
				return fmt.Errorf("%s groups %v, want %v", path, got, groups)
			}
		case "CapInh", "CapPrm", "CapEff", "CapAmb":
			seen[key] = true
			if strings.Trim(value, " \t0") != "" {
				return fmt.Errorf("%s %s = %q, want 0", path, key, strings.TrimSpace(value))
			}
		case "CapBnd":
			seen[key] = true
			if strings.Trim(value, " \t0") == "" {
				return fmt.Errorf("%s CapBnd = %q, want non-zero bounding set", path, strings.TrimSpace(value))
			}
		case "NoNewPrivs":
			seen[key] = true
			if len(fields) != 1 || fields[0] != wantNoNewPrivs {
				return fmt.Errorf("%s NoNewPrivs = %q, want %s", path, strings.TrimSpace(value), wantNoNewPrivs)
			}
		}
	}
	for _, required := range []string{"Uid", "Gid", "Groups", "CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb", "NoNewPrivs"} {
		if !seen[required] {
			return fmt.Errorf("%s missing %s", path, required)
		}
	}
	return nil
}
