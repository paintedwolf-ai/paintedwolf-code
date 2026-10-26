package main

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDedicatedServiceRequiresExactlyTheVerificationCapability(t *testing.T) {
	const ptrace = uint64(1) << unix.CAP_SYS_PTRACE
	safe := dedicatedProtection{uids: [4]int{999, 999, 999, 999}, gids: [4]int{999, 999, 999, 999}, effective: ptrace, permitted: ptrace, inheritable: ptrace, bounding: ptrace, ambient: ptrace, noNewPrivileges: 1}
	testutil.FailErr(t, "accept dedicated service", safe.validate())
	primaryOnly := safe
	primaryOnly.groups = []int{safe.gids[0]}
	testutil.FailErr(t, "accept systemd primary group membership", primaryOnly.validate())
	cases := map[string]func(*dedicatedProtection){
		"effective root uid":   func(f *dedicatedProtection) { f.uids[1] = 0 },
		"saved root uid":       func(f *dedicatedProtection) { f.uids[2] = 0 },
		"filesystem root uid":  func(f *dedicatedProtection) { f.uids[3] = 0 },
		"effective root gid":   func(f *dedicatedProtection) { f.gids[1] = 0 },
		"saved foreign gid":    func(f *dedicatedProtection) { f.gids[2] = 1001 },
		"filesystem root gid":  func(f *dedicatedProtection) { f.gids[3] = 0 },
		"root user":            func(f *dedicatedProtection) { f.uids[0] = 0 },
		"root group":           func(f *dedicatedProtection) { f.gids[0] = 0 },
		"supplementary group":  func(f *dedicatedProtection) { f.groups = []int{10} },
		"primary and foreign":  func(f *dedicatedProtection) { f.groups = []int{f.gids[0], 10} },
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
	if facts.uids[0] != os.Getuid() || facts.gids[0] != os.Getgid() {
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

func TestKernelCredentialFieldsAreCompleteAndUnambiguous(t *testing.T) {
	uids, gids, err := credentialIDs([]byte("Name:\tpw\nUid:\t1000\t1000\t1000\t1000\nGid:\t2000\t2000\t2000\t2000\n"))
	testutil.FailErr(t, "read four identities", err)
	if uids != [4]int{1000, 1000, 1000, 1000} || gids != [4]int{2000, 2000, 2000, 2000} {
		t.Fatal("lost credential identity")
	}
	for _, bad := range []string{"", "Uid: 1 1 1 1", "Uid: 1 1 1\nGid: 1 1 1 1", "Uid: 1 1 1 1\nUid: 1 1 1 1\nGid: 1 1 1 1", "Uid: 1 -1 1 1\nGid: 1 1 1 1", "Uid: 1 1 1 4294967296\nGid: 1 1 1 1"} {
		if _, _, err := credentialIDs([]byte(bad)); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
