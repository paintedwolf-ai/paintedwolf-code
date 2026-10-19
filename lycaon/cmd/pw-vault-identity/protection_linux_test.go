package main

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDedicatedServiceRequiresExactlyTheVerificationCapability(t *testing.T) {
	const ptrace = uint64(1) << unix.CAP_SYS_PTRACE
	safe := dedicatedProtection{uid: 999, gid: 999, effective: ptrace, permitted: ptrace, inheritable: ptrace, bounding: ptrace, ambient: ptrace, noNewPrivileges: 1}
	testutil.FailErr(t, "accept dedicated service", safe.validate())
	primaryOnly := safe
	primaryOnly.groups = []int{safe.gid}
	testutil.FailErr(t, "accept systemd primary group membership", primaryOnly.validate())
	cases := map[string]func(*dedicatedProtection){
		"root user":            func(f *dedicatedProtection) { f.uid = 0 },
		"root group":           func(f *dedicatedProtection) { f.gid = 0 },
		"supplementary group":  func(f *dedicatedProtection) { f.groups = []int{10} },
		"primary and foreign":  func(f *dedicatedProtection) { f.groups = []int{f.gid, 10} },
		"privilege escalation": func(f *dedicatedProtection) { f.noNewPrivileges = 0 },
		"missing effective":    func(f *dedicatedProtection) { f.effective = 0 },
		"extra permitted":      func(f *dedicatedProtection) { f.permitted |= 1 << unix.CAP_DAC_OVERRIDE },
		"extra inheritable":    func(f *dedicatedProtection) { f.inheritable |= 1 << unix.CAP_NET_ADMIN },
		"extra bounding":       func(f *dedicatedProtection) { f.bounding |= 1 << unix.CAP_SETPCAP },
		"extra ambient":        func(f *dedicatedProtection) { f.ambient |= 1 << unix.CAP_CHOWN },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			facts := safe
			mutate(&facts)
			if facts.validate() == nil {
				t.Fatal("unsafe service accepted")
			}
		})
	}
}

func TestDedicatedProtectionReadsActualKernelFacts(t *testing.T) {
	facts, err := readDedicatedProtection()
	testutil.FailErr(t, "read protection", err)
	if facts.uid != os.Getuid() || facts.gid != os.Getgid() {
		t.Fatal("wrong process identity")
	}
	if (requireDedicatedProtection() == nil) != (facts.validate() == nil) {
		t.Fatal("startup did not use the actual protection facts")
	}
}

func TestNegativeIdleExitIsRefusedBeforeListening(t *testing.T) {
	if err := run([]string{"--state", t.TempDir(), "--idle-exit=-1s"}); err == nil {
		t.Fatal("negative idle shutdown accepted")
	}
}
