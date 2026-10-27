package osprocess

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

const dumpsFixtureEnv = "PW_TEST_DUMPS_FIXTURE"

// unprivilegedUID runs both sides of the check as one ordinary user: the
// caller's own uid, or nobody when the suite runs as root, whose authority
// would answer a different question.
const unprivilegedUID = 65534

// TestDumpsFixtureProcess is the body of the target and inspector processes;
// it does nothing unless a test started it.
func TestDumpsFixtureProcess(t *testing.T) {
	switch os.Getenv(dumpsFixtureEnv) {
	case "":
		return
	case "protected", "control":
		if os.Getenv(dumpsFixtureEnv) == "protected" {
			if err := DisableDumps(); err != nil {
				fmt.Fprintln(os.Stdout, "hardening:", err)
				os.Exit(2)
			}
		}
		fmt.Fprintln(os.Stdout, "ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "inspect":
		// The inspector starts its own target, so it is the ancestor Yama's
		// default scope lets attach: only the target's dumpability decides.
		os.Exit(inspectOwnTarget(os.Getenv("PW_TEST_DUMPS_TARGET_MODE")))
	}
}

func inspectOwnTarget(mode string) int {
	target := exec.Command(os.Args[0], "-test.run=^TestDumpsFixtureProcess$")
	target.Env = append(os.Environ(), dumpsFixtureEnv+"="+mode)
	input, err := target.StdinPipe()
	if err != nil {
		return 2
	}
	output, err := target.StdoutPipe()
	if err != nil || target.Start() != nil {
		return 2
	}
	defer func() { _ = input.Close(); _ = target.Wait() }()
	if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "ready\n" {
		fmt.Printf("target: %q %v\n", line, err)
		return 2
	}
	pid := target.Process.Pid
	for _, name := range []string{"mem", "environ", "maps"} {
		file, err := os.Open("/proc/" + strconv.Itoa(pid) + "/" + name)
		if err == nil {
			_ = file.Close()
		}
		fmt.Printf("%s: %v\n", name, err == nil)
	}
	attached := unix.PtraceAttach(pid) == nil
	if attached {
		var status unix.WaitStatus
		_, _ = unix.Wait4(pid, &status, 0, nil)
		_ = unix.PtraceDetach(pid)
	}
	fmt.Printf("ptrace: %v\n", attached)
	return 0
}

// fixtureBinary is the test binary, copied where the unprivileged user can
// execute it when the suite runs as root.
func fixtureBinary(t *testing.T) (string, *syscall.Credential) {
	t.Helper()
	if os.Geteuid() != 0 {
		return os.Args[0], nil
	}
	dir := t.TempDir()
	testutil.FailErr(t, "open fixture directory", os.Chmod(dir, 0o755))
	raw, err := os.ReadFile(os.Args[0])
	testutil.FailErr(t, "read test binary", err)
	path := filepath.Join(dir, "dumps-fixture")
	testutil.FailErr(t, "write fixture binary", os.WriteFile(path, raw, 0o755))
	return path, &syscall.Credential{Uid: unprivilegedUID, Gid: unprivilegedUID}
}

func inspectDumps(t *testing.T, binary string, credential *syscall.Credential, mode string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestDumpsFixtureProcess$")
	cmd.Env = append(os.Environ(), dumpsFixtureEnv+"=inspect", "PW_TEST_DUMPS_TARGET_MODE="+mode)
	cmd.Dir = filepath.Dir(binary)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: credential}
	out, err := cmd.Output()
	testutil.FailErr(t, "run same-user inspector for a "+mode+" target", err)
	return string(out)
}

func TestNonDumpableProcessRefusesSameUserMemoryAndPtrace(t *testing.T) {
	binary, credential := fixtureBinary(t)
	// Control: the same inspector reads an ordinary process of its user, so the
	// refusal below is the hardening and not the fixture.
	control := inspectDumps(t, binary, credential, "control")
	for _, want := range []string{"mem: true", "environ: true", "maps: true", "ptrace: true"} {
		if !strings.Contains(control, want) {
			t.Fatalf("same-user inspector could not reach an ordinary process (want %q):\n%s", want, control)
		}
	}
	protected := inspectDumps(t, binary, credential, "protected")
	for _, want := range []string{"mem: false", "environ: false", "maps: false", "ptrace: false"} {
		if !strings.Contains(protected, want) {
			t.Fatalf("same-user inspector reached a non-dumpable process (want %q):\n%s", want, protected)
		}
	}
}
