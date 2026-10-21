package osprocess

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNonDumpableProcessRefusesSameUserMemoryAndPtrace(t *testing.T) {
	const childEnv = "PW_TEST_NON_DUMPABLE_CHILD"
	if os.Getenv(childEnv) == "1" {
		if err := DisableDumps(); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(os.Stdout, "protected")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	if os.Geteuid() == 0 {
		t.Skip("root has authority to inspect protected processes")
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestNonDumpableProcessRefusesSameUserMemoryAndPtrace$")
	cmd.Env = append(os.Environ(), childEnv+"=1")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = cmd.Wait() })
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || line != "protected\n" {
		t.Fatalf("child hardening: %q, %v", line, err)
	}
	for _, name := range []string{"mem", "environ", "maps"} {
		path := "/proc/" + strconv.Itoa(cmd.Process.Pid) + "/" + name
		file, err := os.Open(path)
		if err == nil {
			_ = file.Close()
			t.Errorf("same-user ancestor opened %s", name)
		}
	}
	if err := unix.PtraceAttach(cmd.Process.Pid); err == nil {
		var status unix.WaitStatus
		_, _ = unix.Wait4(cmd.Process.Pid, &status, 0, nil)
		_ = unix.PtraceDetach(cmd.Process.Pid)
		t.Fatal("same-user ancestor attached to protected process")
	}
}
