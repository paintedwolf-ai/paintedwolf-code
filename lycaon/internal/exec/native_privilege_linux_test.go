//go:build linux && nativeprivileges

package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/osprocess"
	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

const privilegePayloadMarker = "PW_NATIVE_PRIVILEGE_PAYLOAD=1"

type nativePrivilegeFacts struct {
	Credentials               osprocess.ProcessSecurity
	Arguments                 []string
	Directory, KernelFilename string
}

func TestNativePrivilegePayload(t *testing.T) {
	if os.Getenv("PW_NATIVE_PRIVILEGE_PAYLOAD") != "1" {
		return
	}
	if len(os.Args) != 4 {
		os.Exit(2)
	}
	security, err := osprocess.ProcessCredentials(os.Getpid())
	if err != nil {
		os.Exit(3)
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var native [2]unix.CapUserData
	if unix.Capget(&header, &native[0]) != nil || security.Linux == nil || security.Linux.Effective != (uint64(native[0].Effective)|uint64(native[1].Effective)<<32) {
		os.Exit(9)
	}
	cwd, err := os.Getwd()
	if err != nil {
		os.Exit(4)
	}
	filename, err := linuxPayloadExecfn()
	if err != nil {
		os.Exit(5)
	}
	if err := os.WriteFile(os.Args[3], []byte("native privileged effect\n"), 0644); err != nil {
		os.Exit(6)
	}
	if err := os.Chmod(os.Args[3], 0644); err != nil {
		os.Exit(8)
	}
	facts := nativePrivilegeFacts{security, os.Args, cwd, filename}
	if json.NewEncoder(os.Stdout).Encode(facts) != nil {
		os.Exit(7)
	}
	os.Exit(0)
}

func TestLinuxNativePrivilegeQualification(t *testing.T) {
	// No root mutation occurs outside the explicitly selected ephemeral CI lane.
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("PW_NATIVE_PRIVILEGE_QUALIFICATION") != "ci" {
		t.Fatal("NATIVE_PRIVILEGE_SETUP_UNAVAILABLE: explicit ephemeral CI privilege setup is absent")
	}
	initial, err := osprocess.ProcessCredentials(os.Getpid())
	testutil.FailErr(t, "observe initiating native credentials", err)
	if initial.EffectiveUID == 0 || initial.EffectiveGID == 0 || initial.Linux == nil || initial.Linux.NoNewPrivileges || initial.Linux.Effective&(1<<10) != 0 {
		t.Fatal("NATIVE_PRIVILEGE_SETUP_UNAVAILABLE: baseline must be unprivileged and permit a native transition")
	}
	self, err := os.Executable()
	testutil.FailErr(t, "locate synthetic native image", err)
	image, err := os.ReadFile(self)
	testutil.FailErr(t, "capture synthetic native image", err)
	for _, kind := range []string{"setuid", "setgid", "file capability"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			entry := filepath.Join(root, "synthetic-native-identity")
			testutil.FailErr(t, "create synthetic native image", os.WriteFile(entry, image, 0700))
			preparePrivilegedFixture(t, kind, entry)
			baselineEffect := filepath.Join(root, "baseline-effect")
			baseline := nativePrivilegeBaseline(t, entry, root, baselineEffect)
			requireNativePrivilegeGain(t, kind, initial, baseline.Credentials)
			preparedEffect := filepath.Join(root, "prepared-effect")
			boundary := &confine.Confinement{HostExecution: true}
			identity := confine.EgressCommand{SessionID: "synthetic-session", ToolCallID: "native-privilege", ProjectDir: root, Image: entry}
			lease, err := confine.BindAction(boundary, identity)
			testutil.FailErr(t, "admit actual host-execution action", err)
			defer lease.Close(context.Background())
			plan := AgentLaunch(LaunchAgentCommand, "synthetic native privilege qualification", boundary)
			plan.CurrentAuthority = func(ctx context.Context) error { return lease.ValidateLaunch(ctx, boundary, identity) }
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd, cleanup, err := PrepareCommand(ctx, entry, privilegeArguments(preparedEffect), ExecOpts{Launch: plan, Dir: root, Env: privilegeEnvironment()})
			testutil.FailErr(t, "prepare native privileged invocation", err)
			defer cleanup()
			var output bytes.Buffer
			cmd.Stdout = &output
			testutil.FailErr(t, "execute through typed native door", RunPrepared(cmd))
			var prepared nativePrivilegeFacts
			testutil.FailErr(t, "decode actual prepared native facts", json.Unmarshal(output.Bytes(), &prepared))
			if !reflect.DeepEqual(prepared.Credentials, baseline.Credentials) {
				t.Fatal("prepared native credentials/capability sets differ from actual pathname baseline")
			}
			requirePrivilegePayload(t, prepared, entry, root, preparedEffect)
			if nativeEffectOwnership(t, baselineEffect) != nativeEffectOwnership(t, preparedEffect) {
				t.Fatal("actual privileged effect ownership changed")
			}
			assertRevokedPrivilegeLaunch(t, plan, lease, entry, root)
			t.Log("qualified actual native pathname privilege transition and exact effect ownership")
		})
	}
}

func privilegeArguments(effect string) []string {
	return []string{"-test.run=^TestNativePrivilegePayload$", "literal argument", effect}
}
func privilegeEnvironment() []string { return []string{"PATH=/usr/bin:/bin", privilegePayloadMarker} }

func preparePrivilegedFixture(t *testing.T, kind, entry string) {
	t.Helper()
	sudo, err := osexec.LookPath("sudo")
	if err != nil {
		t.Fatal("NATIVE_PRIVILEGE_SETUP_UNAVAILABLE: noninteractive privilege tool absent")
	}
	run := func(name string, args ...string) {
		t.Helper()
		program, err := osexec.LookPath(name)
		if err != nil {
			t.Fatalf("NATIVE_PRIVILEGE_SETUP_UNAVAILABLE: fixture %s tool absent", name)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		command := osexec.CommandContext(ctx, sudo, append([]string{"-n", program}, args...)...)
		if err := command.Run(); err != nil {
			t.Fatalf("NATIVE_PRIVILEGE_SETUP_UNAVAILABLE: numeric fixture %s failed: %v", name, err)
		}
	}
	run("chown", "--", "0:0", entry)
	switch kind {
	case "setuid":
		run("chmod", "--", "4755", entry)
	case "setgid":
		run("chmod", "--", "2755", entry)
	case "file capability":
		run("chmod", "--", "0755", entry)
		run("setcap", "cap_net_bind_service=ep", entry)
	default:
		t.Fatal("unknown native fixture kind")
	}
}

func nativePrivilegeBaseline(t *testing.T, entry, root, effect string) nativePrivilegeFacts {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	// This counterfactual deliberately calls the actual kernel pathname primitive,
	// independently of the product launch adapter being qualified.
	command := osexec.CommandContext(ctx, entry, privilegeArguments(effect)...)
	command.Dir = root
	command.Env = privilegeEnvironment()
	output, err := command.Output()
	if err != nil {
		t.Fatalf("NATIVE_PRIVILEGE_SETUP_UNAVAILABLE: native pathname baseline failed: %v", err)
	}
	var facts nativePrivilegeFacts
	testutil.FailErr(t, "decode independent native baseline", json.Unmarshal(output, &facts))
	requirePrivilegePayload(t, facts, entry, root, effect)
	return facts
}

func requireNativePrivilegeGain(t *testing.T, kind string, initial, actual osprocess.ProcessSecurity) {
	t.Helper()
	qualified := false
	switch kind {
	case "setuid":
		qualified = actual.RealUID == initial.RealUID && actual.EffectiveUID == 0 && actual.SavedUID == 0
	case "setgid":
		qualified = actual.RealGID == initial.RealGID && actual.EffectiveGID == 0 && actual.SavedGID == 0
	case "file capability":
		qualified = actual.EffectiveUID == initial.EffectiveUID && actual.Linux != nil && actual.Linux.Permitted&(1<<10) != 0 && actual.Linux.Effective&(1<<10) != 0
	}
	if !qualified {
		t.Fatalf("NATIVE_PRIVILEGE_SETUP_UNAVAILABLE: actual %s baseline did not gain the required native privilege", kind)
	}
}

func requirePrivilegePayload(t *testing.T, facts nativePrivilegeFacts, entry, root, effect string) {
	t.Helper()
	if !reflect.DeepEqual(facts.Arguments, append([]string{entry}, privilegeArguments(effect)...)) || facts.Directory != root || facts.KernelFilename != entry {
		t.Fatal("native privileged pathname/vector/cwd changed")
	}
	content, err := os.ReadFile(effect)
	testutil.FailErr(t, "observe authoritative native effect bytes", err)
	if string(content) != "native privileged effect\n" {
		t.Fatal("native privileged effect bytes differ")
	}
}

func nativeEffectOwnership(t *testing.T, path string) [2]uint32 {
	t.Helper()
	info, err := os.Stat(path)
	testutil.FailErr(t, "observe actual effect owner", err)
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("native effect ownership unavailable")
	}
	return [2]uint32{stat.Uid, stat.Gid}
}

func assertRevokedPrivilegeLaunch(t *testing.T, plan LaunchPlan, lease *confine.ActionLease, entry, root string) {
	t.Helper()
	effect := filepath.Join(root, "revoked-effect")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd, cleanup, err := PrepareCommand(ctx, entry, privilegeArguments(effect), ExecOpts{Launch: plan, Dir: root, Env: privilegeEnvironment()})
	testutil.FailErr(t, "capture currently admitted privileged image", err)
	defer cleanup()
	var output bytes.Buffer
	cmd.Stdout = &output
	err = StartPreparedWith(cmd, cmd.Start, func(pid int) func() { release := TrackProcess(pid); lease.Close(context.Background()); return release })
	if err == nil || cmd.ProcessState == nil || output.Len() != 0 {
		t.Fatal("revoked privileged action reported accepted execution")
	}
	if _, err := os.Stat(effect); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("revoked privileged action committed an effect")
	}
}
