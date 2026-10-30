//go:build linux

package launchguard

import (
	"debug/elf"
	"encoding/binary"
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
	raw, err := os.ReadFile(image)
	testutil.FailErr(t, "read compiled transition", err)
	badMachine := append([]byte(nil), raw...)
	binary.LittleEndian.PutUint16(badMachine[18:], uint16(elf.EM_NONE))
	badTable := append([]byte(nil), raw...)
	binary.LittleEndian.PutUint64(badTable[40:], uint64(len(raw)-1))
	badEntry := append([]byte(nil), raw...)
	phoff := binary.LittleEndian.Uint64(raw[32:])
	phsize := uint64(binary.LittleEndian.Uint16(raw[54:]))
	phnum := uint64(binary.LittleEndian.Uint16(raw[56:]))
	for index := uint64(0); index < phnum; index++ {
		row := phoff + index*phsize
		if binary.LittleEndian.Uint32(raw[row:]) == uint32(elf.PT_LOAD) && binary.LittleEndian.Uint32(raw[row+4:])&uint32(elf.PF_X) != 0 {
			fileSize := binary.LittleEndian.Uint64(raw[row+32:])
			address := binary.LittleEndian.Uint64(raw[row+16:])
			binary.LittleEndian.PutUint64(badEntry[24:], address+fileSize)
			binary.LittleEndian.PutUint64(badEntry[row+40:], fileSize+1)
			break
		}
	}
	for name, content := range map[string][]byte{"wrong machine": badMachine, "truncated section table": badTable, "entry in executable zero fill": badEntry} {
		t.Run(name, func(t *testing.T) {
			invalid := filepath.Join(t.TempDir(), "invalid-elf")
			testutil.FailErr(t, "write invalid transition", os.WriteFile(invalid, content, 0o600))
			command := exec.Command("python3", "-c", "from pathlib import Path; import sys; from scripts.linux_release.mount_intake import inspect; inspect(Path(sys.argv[1]).read_bytes(),sys.argv[2])", invalid, runtime.GOARCH)
			command.Dir = filepath.Join("..", "..", "..")
			if _, err := command.CombinedOutput(); err == nil {
				t.Fatal("accepted invalid native transition metadata")
			}
		})
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
			if err == nil || command.ProcessState == nil || command.ProcessState.ExitCode() != 125 || len(out) != 0 {
				t.Fatalf("unprivileged transition: state=%v err=%v output=%q", command.ProcessState, err, out)
			}
		})
	}
}
