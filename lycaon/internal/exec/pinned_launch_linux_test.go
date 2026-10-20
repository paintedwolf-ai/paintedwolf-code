//go:build linux

package exec

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/osprocess"
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
		if unix.Fstat(fd, &stat) == nil && stat.Mode&unix.S_IFMT == unix.S_IFSOCK {
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

func TestPinnedLinuxBinaryUsesHeldImageLiteralVectorCwdAndOwnedGuard(t *testing.T) {
	self, err := os.Executable()
	testutil.FailErr(t, "locate native fixture", err)
	for _, host := range []bool{true, false} {
		t.Run(map[bool]string{true: "host", false: "confined"}[host], func(t *testing.T) {
			if !host && !confine.Available() {
				t.Skip("native Linux confinement unavailable; host primitive fixture remains separate")
			}
			root := t.TempDir()
			paths := &linuxPinnedPaths{cwd: root, entry: self}
			pins, err := confine.CaptureProcessPins(t.Context(), self, paths, confine.LaunchVectorLimits{ArgumentBytes: 128 << 10, ArgvBytes: 1 << 20, EnvironmentBytes: 1 << 20, ExpandedFields: 16384})
			testutil.FailErr(t, "capture native image", err)
			defer pins.Close()
			if pins.LaunchMechanism() != confine.HeldBinaryLaunch || !errors.Is(pins.RequireStoppedObservation(), osprocess.ErrExecutableObservationUnavailable) {
				t.Fatal("mechanism pretended to offer stopped observation")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			plan := AgentLaunch(LaunchAgentCommand, "synthetic held binary fixture", &confine.Confinement{HostExecution: host, Roots: []string{root}, Network: confine.NetworkDeny}).WithPinnedIdentity(pins)
			args := []string{"-test.run=^TestPinnedLinuxPayload$", "--", "literal space", "--pin-ready-fd", "user data"}
			cmd, cleanup, err := PrepareCommand(ctx, "synthetic-argv-zero", args, ExecOpts{Launch: plan, Dir: root, Env: []string{"PATH=/usr/bin:/bin", "PW_PINNED_PAYLOAD=1"}})
			testutil.FailErr(t, "prepare native fixture", err)
			defer cleanup()
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := RunInOwnGroup(cmd); err != nil {
				t.Fatalf("native held launch: %v; diagnostic %s", err, stderr.String())
			}
			var payload linuxPinnedPayload
			testutil.FailErr(t, "decode actual native facts", json.Unmarshal(stdout.Bytes(), &payload))
			if !slices.Equal(payload.Args, append([]string{"synthetic-argv-zero"}, args...)) || payload.Cwd != root || len(payload.ProtocolFDs) != 0 {
				t.Fatalf("literal vector/cwd/private descriptors changed: %+v", payload)
			}
			facts, ok := pins.LaunchFacts()
			if !ok || facts.Mechanism != confine.HeldBinaryLaunch || !facts.SelectedEntryBound || !facts.DirectoryBound || !facts.ArgumentsBound || !facts.EnvironmentBound || facts.PID <= 0 {
				t.Fatal("missing actual selected launch facts")
			}
			if payload.AuxExecutable != facts.AuxiliaryExecutableName || !strings.HasPrefix(payload.AuxExecutable, "/proc/self/fd/") {
				t.Fatal("native auxiliary vector was silently asserted equivalent to pathname exec")
			}
			if _, err := strconv.Atoi(strings.TrimPrefix(payload.AuxExecutable, "/proc/self/fd/")); err != nil {
				t.Fatal("auxiliary descriptor number missing")
			}
		})
	}
}

func TestPinnedLinuxScriptCannotSilentlySelectBinaryDescriptorTransport(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "fixture-script")
	testutil.FailErr(t, "write script", os.WriteFile(script, []byte("#!/bin/sh\nprintf forbidden > output\n"), 0700))
	// Capture the real interpreter too; launch rejection must be a capability result.
	paths := &linuxScriptPaths{linuxPinnedPaths{cwd: root, entry: script}}
	pins, err := confine.CaptureProcessPins(t.Context(), script, paths, confine.LaunchVectorLimits{ArgumentBytes: 128 << 10, ArgvBytes: 1 << 20, EnvironmentBytes: 1 << 20, ExpandedFields: 16384})
	testutil.FailErr(t, "capture native script", err)
	defer pins.Close()
	plan := AgentLaunch(LaunchAgentCommand, "synthetic script capability refusal", &confine.Confinement{HostExecution: true}).WithPinnedIdentity(pins)
	cmd, cleanup, err := PrepareCommand(t.Context(), script, nil, ExecOpts{Launch: plan})
	if cleanup != nil {
		cleanup()
	}
	if cmd != nil || !errors.Is(err, osprocess.ErrExecutableObservationUnavailable) {
		t.Fatal("script transport silently changed")
	}
	if _, err := os.Stat(filepath.Join(root, "output")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused script produced effects")
	}
}

type linuxScriptPaths struct{ linuxPinnedPaths }

func (p *linuxScriptPaths) OpenExecutable(ctx context.Context, name string) (*os.File, error) {
	if name == "/bin/sh" {
		return os.Open(name)
	}
	return p.linuxPinnedPaths.OpenExecutable(ctx, name)
}
