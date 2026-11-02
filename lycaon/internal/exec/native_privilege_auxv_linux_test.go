//go:build linux && nativeprivileges

package exec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func linuxPayloadExecfn() (string, error) {
	// Privilege transitions can restrict /proc access; use the startup vector.
	vector, err := unix.Auxv()
	if err != nil {
		return "", fmt.Errorf("read native auxiliary vector: %w", err)
	}
	for _, entry := range vector {
		if entry[0] != 31 { // AT_EXECFN
			continue
		}
		var buffer [4096]byte
		local := unix.Iovec{Base: &buffer[0]}
		local.SetLen(len(buffer))
		n, readErr := unix.ProcessVMReadv(os.Getpid(), []unix.Iovec{local},
			[]unix.RemoteIovec{{Base: entry[1], Len: len(buffer)}}, 0)
		if n > 0 {
			if zero := bytes.IndexByte(buffer[:n], 0); zero >= 0 {
				return string(buffer[:zero]), nil
			}
		}
		if readErr != nil {
			return "", fmt.Errorf("read native executable filename: %w", readErr)
		}
		return "", errors.New("unterminated native executable filename")
	}
	return "", errors.New("native AT_EXECFN absent")
}

func TestNativePrivilegeAuxvProbe(t *testing.T) {
	if os.Getenv("PW_NATIVE_AUXV_PROBE") != "1" {
		return
	}
	filename, err := linuxPayloadExecfn()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(filename); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestNativePrivilegeAuxvSeparatesKernelFilenameFromArgvZero(t *testing.T) {
	self, err := os.Executable()
	testutil.FailErr(t, "locate native auxiliary-vector fixture", err)
	command := osexec.CommandContext(t.Context(), self, "-test.run=^TestNativePrivilegeAuxvProbe$")
	command.Args[0] = "synthetic-argument-zero"
	command.Env = append(os.Environ(), "PW_NATIVE_AUXV_PROBE=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("observe actual native auxiliary vector: %v\n%s", err, output)
	}
	var filename string
	testutil.FailErr(t, "decode native kernel filename", json.Unmarshal(output, &filename))
	if filename != self {
		t.Fatalf("kernel executable filename: got %q, want %q", filename, self)
	}
}
