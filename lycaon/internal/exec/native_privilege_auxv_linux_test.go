//go:build linux && nativeprivileges

package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
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

func TestNativePrivilegeUnprivilegedPayloadThroughAgentDoor(t *testing.T) {
	self, err := os.Executable()
	testutil.FailErr(t, "locate native privilege payload", err)
	root := t.TempDir()
	baselineEffect := filepath.Join(root, "baseline-effect")
	baseline := nativePrivilegeBaseline(t, self, root, baselineEffect)
	boundary := &confine.Confinement{HostExecution: true}
	identity := confine.EgressCommand{SessionID: "synthetic-session", ToolCallID: "unprivileged-payload", ProjectDir: root, Image: self}
	lease, err := confine.BindAction(boundary, identity)
	testutil.FailErr(t, "admit unprivileged native fixture", err)
	defer lease.Close(context.Background())
	plan := AgentLaunch(LaunchAgentCommand, "native fixture environment", boundary)
	plan.CurrentAuthority = func(ctx context.Context) error { return lease.ValidateLaunch(ctx, boundary, identity) }
	effect := filepath.Join(root, "prepared-effect")
	cmd, cleanup, err := PrepareCommand(t.Context(), self, privilegeArguments(effect), privilegeOptions(plan, root))
	testutil.FailErr(t, "prepare unprivileged native fixture", err)
	defer cleanup()
	var output bytes.Buffer
	cmd.Stdout = &output
	testutil.FailErr(t, "execute unprivileged native fixture", RunPrepared(t.Context(), cmd))
	var facts nativePrivilegeFacts
	testutil.FailErr(t, "decode unprivileged native facts", json.Unmarshal(output.Bytes(), &facts))
	requirePrivilegePayload(t, facts, self, root, effect)
	if facts.Credentials.EffectiveUID != baseline.Credentials.EffectiveUID || facts.Credentials.EffectiveGID != baseline.Credentials.EffectiveGID {
		t.Fatal("unprivileged fixture changed effective identity")
	}
}
