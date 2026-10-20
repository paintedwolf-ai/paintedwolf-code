//go:build linux

package exec

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
	"unsafe"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

type linuxPinnedPaths struct {
	cwd, entry string
	revoked    bool
}

func (p *linuxPinnedPaths) OpenExecutable(ctx context.Context, name string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.revoked || name != p.entry {
		return nil, os.ErrPermission
	}
	return os.Open(name)
}
func (p *linuxPinnedPaths) OpenCwd(ctx context.Context) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.revoked {
		return nil, os.ErrPermission
	}
	return os.Open(p.cwd)
}

type linuxPinnedPayload struct {
	Args               []string
	Cwd, AuxExecutable string
	ProtocolFDs        []int
}

func TestPinnedLinuxPayload(t *testing.T) {
	if os.Getenv("PW_PINNED_PAYLOAD") != "1" {
		return
	}
	cwd, err := os.Getwd()
	testutil.FailErr(t, "observe native cwd", err)
	aux, err := linuxPayloadExecfn()
	testutil.FailErr(t, "observe native AT_EXECFN", err)
	payload := linuxPinnedPayload{Args: os.Args, Cwd: cwd, AuxExecutable: aux}
	// The final trusted helper's image/control descriptors are all below16.
	for fd := 3; fd < 16; fd++ {
		var stat unix.Stat_t
		if unix.Fstat(fd, &stat) == nil && (stat.Mode&unix.S_IFMT == unix.S_IFSOCK || stat.Mode&unix.S_IFMT == unix.S_IFIFO || stat.Mode&unix.S_IFMT == unix.S_IFREG) {
			payload.ProtocolFDs = append(payload.ProtocolFDs, fd)
		}
	}
	raw, err := json.Marshal(payload)
	testutil.FailErr(t, "encode native payload facts", err)
	_, err = os.Stdout.Write(raw)
	if err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

// Read the kernel auxiliary vector and the exact pointed-to executable name.
// No reconstructed argv value substitutes for the actual AT_EXECFN observation.
func linuxPayloadExecfn() (string, error) {
	data, err := os.ReadFile("/proc/self/auxv")
	if err != nil {
		return "", err
	}
	word := int(unsafe.Sizeof(uintptr(0)))
	for offset := 0; offset+2*word <= len(data); offset += 2 * word {
		var key, value uint64
		if word == 8 {
			key = binary.NativeEndian.Uint64(data[offset:])
			value = binary.NativeEndian.Uint64(data[offset+word:])
		} else {
			key = uint64(binary.NativeEndian.Uint32(data[offset:]))
			value = uint64(binary.NativeEndian.Uint32(data[offset+word:]))
		}
		if key != 31 {
			continue
		}
		file, err := os.Open("/proc/self/mem")
		if err != nil {
			return "", err
		}
		defer file.Close()
		var buffer [4096]byte
		n, err := file.ReadAt(buffer[:], int64(value))
		if zero := bytes.IndexByte(buffer[:n], 0); zero >= 0 {
			return string(buffer[:zero]), nil
		}
		return "", fmt.Errorf("unterminated native execfn: %w", err)
	}
	return "", errors.New("native AT_EXECFN absent")
}

func TestPreparedLinuxBinaryPreservesNativePathArgvCwdAndExecuteOnlyMode(t *testing.T) {
	self, err := os.Executable()
	testutil.FailErr(t, "locate native fixture", err)
	bytes, err := os.ReadFile(self)
	testutil.FailErr(t, "read source fixture", err)
	root := t.TempDir()
	entry := filepath.Join(root, "synthetic-argv-zero")
	testutil.FailErr(t, "create execution-only native fixture", os.WriteFile(entry, bytes, 0100))
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	args := []string{"-test.run=^TestPinnedLinuxPayload$", "--", "literal space", "--launch-ready-fd", "user data"}
	plan := HostLaunch("synthetic native binary fixture")
	cmd, cleanup, err := PrepareCommand(ctx, "synthetic-argv-zero", args, ExecOpts{Launch: plan, Dir: root, Env: []string{"PATH=" + root + ":/usr/bin:/bin", "PW_PINNED_PAYLOAD=1"}})
	testutil.FailErr(t, "prepare native fixture", err)
	defer cleanup()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := RunInOwnGroup(cmd); err != nil {
		t.Fatalf("native launch: %v; diagnostic %s", err, stderr.String())
	}
	var payload linuxPinnedPayload
	testutil.FailErr(t, "decode actual native facts", json.Unmarshal(stdout.Bytes(), &payload))
	if !slices.Equal(payload.Args, append([]string{"synthetic-argv-zero"}, args...)) || payload.Cwd != root || len(payload.ProtocolFDs) != 0 {
		t.Fatalf("native vector/cwd/private descriptors changed: %+v", payload)
	}
	if payload.AuxExecutable != entry {
		t.Fatalf("native pathname AT_EXECFN changed: %q", payload.AuxExecutable)
	}
}

func TestPreparedLinuxScriptPreservesOrdinaryPathAndNativeArgv(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "fixture-script")
	testutil.FailErr(t, "write script", os.WriteFile(entry, []byte("#!/bin/sh -e\nprintf '%s\\n' \"$0\" \"$1\"\nprintf exact > effect\n"), 0700))
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	plan := AgentLaunch(LaunchAgentCommand, "synthetic native script fixture", &confine.Confinement{HostExecution: true})
	cmd, cleanup, err := PrepareCommand(ctx, "./fixture-script", []string{"literal space"}, ExecOpts{Launch: plan, Dir: root, Env: []string{"PATH=/usr/bin:/bin"}})
	testutil.FailErr(t, "prepare ordinary script", err)
	defer cleanup()
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	testutil.FailErr(t, "launch through native pathname", RunInOwnGroup(cmd))
	if stdout.String() != "./fixture-script\nliteral space\n" {
		t.Fatalf("script argv changed: %q", stdout.String())
	}
	data, err := os.ReadFile(filepath.Join(root, "effect"))
	testutil.FailErr(t, "read actual script effect", err)
	if string(data) != "exact" {
		t.Fatal("script actual cwd/effect changed")
	}
}
