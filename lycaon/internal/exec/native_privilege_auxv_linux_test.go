//go:build linux && nativeprivileges

package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	osexec "os/exec"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func buildNativePrivilegePayload(t *testing.T) string {
	t.Helper()
	image := filepath.Join(t.TempDir(), "native-privilege-payload")
	build := osexec.CommandContext(t.Context(), "go", "build", "-o", image, "./testdata/native_privilege")
	build.Env = append(os.Environ(), "CGO_ENABLED=1")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build native privilege payload: %v\n%s", err, output)
	}
	return image
}

func TestNativePrivilegeAuxvSeparatesKernelFilenameFromArgvZero(t *testing.T) {
	self := buildNativePrivilegePayload(t)
	root := t.TempDir()
	command := osexec.CommandContext(t.Context(), self, privilegeArguments(filepath.Join(root, "effect"))...)
	command.Args[0] = "synthetic-argument-zero"
	command.Env = append(privilegeEnvironment(), "PW_NATIVE_AUXV_NONDUMPABLE=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("observe actual native auxiliary vector: %v\n%s", err, output)
	}
	var facts nativePrivilegeFacts
	testutil.FailErr(t, "decode native kernel facts", json.Unmarshal(output, &facts))
	if facts.KernelFilename != self || facts.Arguments[0] != "synthetic-argument-zero" {
		t.Fatalf("kernel filename/argv zero: %+v", facts)
	}
}

func TestNativePrivilegeUnprivilegedPayloadThroughAgentDoor(t *testing.T) {
	self := buildNativePrivilegePayload(t)
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
