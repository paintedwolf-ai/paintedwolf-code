//go:build linux

package launchguard

import (
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func buildMountTransition(t *testing.T, output string) {
	t.Helper()
	command := exec.Command("python3", "-m", "scripts.linux_release.mount_intake", "--output", output, "--architecture", runtime.GOARCH)
	command.Dir = filepath.Join("..", "..", "..")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build actual fixed intake transition: %v\n%s", err, out)
	}
}

func TestMountTransitionBuildIsFreestandingAndRefusesUnprivilegedInputs(t *testing.T) {
	image := filepath.Join(t.TempDir(), "pw-mount-intake-transition")
	buildMountTransition(t, image)
	object, err := elf.Open(image)
	testutil.FailErr(t, "inspect fixed transition", err)
	defer func() { _ = object.Close() }()
	machine := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}[runtime.GOARCH]
	if object.Machine != machine || object.Type != elf.ET_EXEC {
		t.Fatalf("transition machine/type %s/%s", object.Machine, object.Type)
	}
	for _, program := range object.Progs {
		if program.Type == elf.PT_INTERP || program.Type == elf.PT_DYNAMIC || program.Type == elf.PT_TLS {
			t.Fatalf("transition uses runtime segment %s", program.Type)
		}
	}
	file, err := os.Open(image)
	testutil.FailErr(t, "retain fixed transition", err)
	defer func() { _ = file.Close() }()
	for name, environment := range map[string][]string{"empty": {}, "caller environment": {"UNTRUSTED_INPUT=1"}} {
		t.Run(name, func(t *testing.T) {
			command := exec.Command("/proc/self/fd/3")
			command.Env, command.ExtraFiles = environment, []*os.File{file}
			if os.Getuid() == 0 {
				command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 1000, Gid: 1000}}
			}
			out, err := command.CombinedOutput()
			if err == nil || command.ProcessState.ExitCode() != 125 || len(out) != 0 {
				t.Fatalf("unprivileged transition: state=%v err=%v output=%q", command.ProcessState, err, out)
			}
		})
	}
}
