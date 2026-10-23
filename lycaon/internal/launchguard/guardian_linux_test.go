//go:build linux

package launchguard

import (
	"encoding/binary"
	"errors"
	"os/exec"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestGuardianRootsRejectsAmbiguousLifecycleAuthority(t *testing.T) {
	raw := make([]byte, guardianRootsBytes)
	copy(raw, "PWGROOT1")
	binary.LittleEndian.PutUint32(raw[8:], 1)
	binary.LittleEndian.PutUint32(raw[12:], 42)
	binary.LittleEndian.PutUint64(raw[16:], 3)
	for index, pid := range []uint64{43, 44, 45} {
		binary.LittleEndian.PutUint64(raw[24+index*8:], pid)
	}
	binary.LittleEndian.PutUint32(raw[48:], 42)
	_, err := decodeGuardianRoots(raw)
	testutil.FailErr(t, "decode exact protected lineage roots", err)
	for name, mutate := range map[string]func([]byte){
		"version":        func(b []byte) { b[8] = 2 },
		"nonleader":      func(b []byte) { b[48] = 43 },
		"zero sequence":  func(b []byte) { b[16] = 0 },
		"duplicate role": func(b []byte) { b[32] = 43 },
		"self role":      func(b []byte) { b[24] = 42 },
		"reserved":       func(b []byte) { b[63] = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			bad := append([]byte(nil), raw...)
			mutate(bad)
			if _, err := decodeGuardianRoots(bad); err == nil {
				t.Fatal("accepted ambiguous guardian authority")
			}
		})
	}
	for size := 0; size < len(raw); size++ {
		if _, err := decodeGuardianRoots(raw[:size]); err == nil {
			t.Fatalf("accepted truncated frame %d", size)
		}
	}
	if _, err := decodeGuardianRoots(append(raw, 0)); err == nil {
		t.Fatal("accepted trailing guardian data")
	}
}

// Exercises the installed setuid bootstrap, fixed static transition and real
// guardian, rather than substituting a stand-in reaper for production code.
func TestGuardianRealLaunchPreservesDesktopExitStatus(t *testing.T) {
	fixture := installLaunchFixture(t)
	for _, test := range []struct {
		name string
		args []string
		code int
	}{
		{"ordinary exit", []string{"--exit-status", "0"}, 0},
		{"nonzero exit", []string{"--exit-status", "3"}, 3},
		{"signal exit", []string{"--self-sigkill"}, 137},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command(fixture.bootstrap, test.args...)
			output, err := command.CombinedOutput()
			code := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatalf("real launch failed: %v\n%s", err, output)
				}
				code = exit.ExitCode()
			}
			if code != test.code {
				t.Fatalf("real guardian exit=%d want=%d: %v\n%s", code, test.code, err, output)
			}
			if err := sudoFixture(t, "lock-free", fixture.dir, fixtureHash+"="+fixture.digest); err != nil {
				t.Fatalf("settled real guardian retained generation lock: %v", err)
			}
		})
	}
}
