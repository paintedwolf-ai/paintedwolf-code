//go:build linux

package launchguard

import (
	"encoding/json"
	"os"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func entryFixture() EntrySpec {
	return EntrySpec{
		Version: 1, SupervisorPID: 10, UID: 1000, GID: 1000,
		Generation:  GenerationPolicy{Version: 1, GenerationRoot: "/opt/painted-wolf-code", LogicalInstallRoot: "/opt/painted-wolf-code", ManifestSHA256: "0000000000000000000000000000000000000000000000000000000000000000", LibraryRoots: []string{"/usr/lib"}},
		Image:       pkgmanifest.Executable{Path: "/opt/painted-wolf-code/bin/painted-wolf-code", Size: 1, SHA256: "0000000000000000000000000000000000000000000000000000000000000000"},
		Environment: []string{"PATH=/usr/bin:/bin"},
	}
}

func TestEntryFrameRejectsIdentityAndTransportAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*EntrySpec)
		valid  bool
	}{
		{"selected desktop", func(*EntrySpec) {}, true},
		{"root identity", func(s *EntrySpec) { s.UID = 0 }, false},
		{"missing supervisor", func(s *EntrySpec) { s.SupervisorPID = 1 }, false},
		{"other role", func(s *EntrySpec) { s.Image.Path = "/usr/bin/sh" }, false},
		{"wrong generation", func(s *EntrySpec) { s.Generation.GenerationRoot = "/opt/other" }, false},
		{"no module policy", func(s *EntrySpec) { s.Generation.LibraryRoots = nil }, false},
		{"invalid supplementary identity", func(s *EntrySpec) { s.Groups = []uint32{^uint32(0)} }, false},
		{"relative image", func(s *EntrySpec) { s.Image.Path = "painted-wolf-code" }, false},
		{"duplicate environment", func(s *EntrySpec) { s.Environment = []string{"A=one", "A=two"} }, false},
		{"environment terminator", func(s *EntrySpec) { s.Environment = []string{"A=one\x00B=two"} }, false},
		{"argument terminator", func(s *EntrySpec) { s.Arguments = []string{"one\x00two"} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := entryFixture()
			tc.mutate(&spec)
			raw, err := json.Marshal(spec)
			testutil.FailErr(t, "encode entry frame", err)
			_, err = decodeEntry(raw)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
	raw, err := json.Marshal(entryFixture())
	testutil.FailErr(t, "encode valid entry", err)
	if _, err := decodeEntry(append(raw, []byte(" {}")...)); err == nil {
		t.Fatal("accepted a second entry frame")
	}
}

func TestInitialNamespaceCannotAttestProtectedEntry(t *testing.T) {
	origin, err := os.Open("/proc/self/ns/user")
	testutil.FailErr(t, "open original namespace", err)
	defer func() { _ = origin.Close() }()
	if err := verifyNamespace(origin); err == nil {
		t.Fatal("accepted the unchanged user namespace as a protected transition")
	}
	if err := verifyNamespace(nil); err == nil {
		t.Fatal("accepted a missing original namespace descriptor")
	}
}

func TestBrokerAgentSealsDumpableAndVerifiesIdentity(t *testing.T) {
	t.Cleanup(func() {
		_ = unix.Prctl(unix.PR_SET_DUMPABLE, 1, 0, 0, 0)
	})
	groups, err := unix.Getgroups()
	testutil.FailErr(t, "groups", err)
	groupIDs := make([]uint32, len(groups))
	for i, g := range groups {
		groupIDs[i] = uint32(g)
	}
	spec := entryFixture()
	spec.UID = uint32(os.Getuid())
	spec.GID = uint32(os.Getgid())
	spec.Groups = groupIDs
	testutil.FailErr(t, "sealBrokerAgentIdentity", sealBrokerAgentIdentity(spec))
	dumpable, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	testutil.FailErr(t, "get dumpable", err)
	if dumpable != 0 {
		t.Fatalf("dumpable = %d, want 0", dumpable)
	}
	testutil.FailErr(t, "initialUserNamespace", initialUserNamespace())
	spec.UID++
	if err := sealBrokerAgentIdentity(spec); err == nil {
		t.Fatal("accepted mismatched caller identity")
	}
	spec.UID = uint32(os.Getuid())
	spec.GID++
	if err := sameIdentity(spec); err == nil {
		t.Fatal("accepted mismatched caller GID")
	}
	spec.GID = uint32(os.Getgid())
	spec.Groups = append([]uint32(nil), groupIDs...)
	spec.Groups = append(spec.Groups, 99999)
	if err := sameIdentity(spec); err == nil {
		t.Fatal("accepted mismatched group count")
	}
	if len(groupIDs) > 0 {
		spec.Groups = append([]uint32(nil), groupIDs...)
		spec.Groups[0]++
		if err := sameIdentity(spec); err == nil {
			t.Fatal("accepted mismatched supplementary group")
		}
	}
	spec.Groups = groupIDs
	if err := verifyBroker(entryBrokerFD, spec); err == nil {
		t.Fatal("verifyBroker accepted non-socket descriptor")
	}

	withTestTrustAnchor(t, t.TempDir())
	spec.SupervisorPID = os.Getpid()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "socketpair", err)
	s0 := os.NewFile(uintptr(pair[0]), "s0")
	s1 := os.NewFile(uintptr(pair[1]), "s1")
	defer func() {
		_ = s0.Close()
		_ = s1.Close()
	}()
	testutil.FailErr(t, "verifyBroker valid socket", verifyBroker(pair[0], spec))

	// runBrokerAgent error and clean-shutdown paths.
	unsealed := filepathJoinTempFile(t, "{}")
	dummyBroker := filepathJoinTempFile(t, "")
	if err := runBrokerAgent(unsealed, dummyBroker); err == nil {
		t.Fatal("runBrokerAgent accepted unsealed frame")
	}

	badSpec := spec
	badSpec.UID++
	badFrame, err := sealEntrySpec(badSpec)
	testutil.FailErr(t, "seal badSpec", err)
	if err := runBrokerAgent(badFrame, filepathJoinTempFile(t, "")); err == nil {
		t.Fatal("runBrokerAgent accepted mismatched UID")
	}

	validFrame1, err := sealEntrySpec(spec)
	testutil.FailErr(t, "seal validFrame1", err)
	if err := runBrokerAgent(validFrame1, filepathJoinTempFile(t, "")); err == nil {
		t.Fatal("runBrokerAgent accepted non-socket broker")
	}

	validFrame2, err := sealEntrySpec(spec)
	testutil.FailErr(t, "seal validFrame2", err)
	_ = s1.Close()
	testutil.FailErr(t, "runBrokerAgent clean EOF", runBrokerAgent(validFrame2, s0))

	// enterDesktop and SealEntry on a dedicated locked OS thread so
	// PR_SET_NO_NEW_PRIVS only affects the terminated thread.
	if err := enterDesktop(filepathJoinTempFile(t, ""), filepathJoinTempFile(t, "{}"), filepathJoinTempFile(t, ""), -1, filepathJoinTempFile(t, "")); err == nil {
		t.Fatal("enterDesktop accepted unsealed frame")
	}
	badFrame2, err := sealEntrySpec(badSpec)
	testutil.FailErr(t, "seal badFrame2", err)
	if err := enterDesktop(filepathJoinTempFile(t, ""), badFrame2, filepathJoinTempFile(t, ""), -1, filepathJoinTempFile(t, "")); err == nil {
		t.Fatal("enterDesktop accepted mismatched UID")
	}
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		vf, err := sealEntrySpec(spec)
		if err != nil {
			done <- err
			return
		}
		orig, err := os.Open("/proc/self/ns/user")
		if err != nil {
			_ = vf.Close()
			done <- err
			return
		}
		img := filepathJoinTempFile(t, "")
		life := filepathJoinTempFile(t, "")
		done <- enterDesktop(img, vf, orig, -1, life)
	}()
	if err := <-done; err == nil {
		t.Fatal("enterDesktop succeeded in initial user namespace")
	}
}
