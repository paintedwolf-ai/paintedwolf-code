//go:build linux

package launchguard

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestGuardianThreadProfileRequiresCompleteCallerAuthorityDrop(t *testing.T) {
	caller := Identity{UID: 1000, GID: 100, Groups: []uint32{10, 100}}
	lines := []string{
		"Uid:\t1000 1000 1000 1000", "Gid:\t100 100 100 100", "Groups:\t10 100",
		"CapInh:\t0000000000000000", "CapPrm:\t0000000000000000",
		"CapEff:\t0000000000000000", "CapBnd:\t0000000000000000", "CapAmb:\t0000000000000000",
		"NoNewPrivs:\t1", "TracerPid:\t0",
	}
	profile := strings.Join(lines, "\n") + "\n"
	testutil.FailErr(t, "accept exact dropped caller profile", guardianThreadProfile([]byte("Name:\tguardian\n"+profile), caller))
	for index, line := range lines {
		key, _, _ := strings.Cut(line, ":")
		t.Run("missing "+key, func(t *testing.T) {
			without := append([]string(nil), lines[:index]...)
			without = append(without, lines[index+1:]...)
			if guardianThreadProfile([]byte(strings.Join(without, "\n")), caller) == nil {
				t.Fatal("accepted incomplete post-transition authority evidence")
			}
		})
		t.Run("duplicate "+key, func(t *testing.T) {
			if guardianThreadProfile([]byte(profile+line+"\n"), caller) == nil {
				t.Fatal("accepted ambiguous duplicated authority evidence")
			}
		})
	}
	bad := map[string]string{
		"groups reordered": "Groups:\t100 10", "groups extra": "Groups:\t10 100 0",
		"groups missing": "Groups:\t10", "groups overflow": "Groups:\t4294967296 100",
		"uid overflow": "Uid:\t4294967296 1000 1000 1000", "uid malformed": "Uid:\tuser 1000 1000 1000",
		"uid cardinality": "Uid:\t1000 1000 1000", "NNP absent": "NoNewPrivs:\t0",
		"traced": "TracerPid:\t42", "tracer ambiguity": "TracerPid:\t0 0",
		"cap overflow": "CapEff:\t10000000000000000", "cap malformed": "CapEff:\txyz",
	}
	for _, key := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"} {
		bad[key+" retained"] = key + ":\t0000000000000001"
	}
	for _, key := range []string{"Uid", "Gid"} {
		for column := 0; column < 4; column++ {
			fields := strings.Fields(strings.SplitN(lines[0], ":", 2)[1])
			if key == "Gid" {
				fields = strings.Fields(strings.SplitN(lines[1], ":", 2)[1])
			}
			fields[column] = "0"
			bad[fmt.Sprintf("%s privileged column %d", key, column)] = key + ":\t" + strings.Join(fields, " ")
		}
	}
	for name, replacement := range bad {
		t.Run(name, func(t *testing.T) {
			key, _, _ := strings.Cut(replacement, ":")
			changed := append([]string(nil), lines...)
			for index, line := range changed {
				if strings.HasPrefix(line, key+":") {
					changed[index] = replacement
				}
			}
			if guardianThreadProfile([]byte(strings.Join(changed, "\n")), caller) == nil {
				t.Fatal("accepted privileged, traced or ambiguous thread profile")
			}
		})
	}
}

func TestGuardianProfileRejectsOriginalNamespace(t *testing.T) {
	origin, err := os.Open("/proc/self/ns/user")
	testutil.FailErr(t, "open original namespace identity", err)
	defer func() { _ = origin.Close() }()
	caller := Identity{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}
	if verifyGuardianProfile(os.Getpid(), caller, origin) == nil {
		t.Fatal("original namespace was accepted as the distinct protected guardian namespace")
	}
}

func guardianMetadataPair(t *testing.T, credentials bool) [2]int {
	t.Helper()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "create guardian protocol pair", err)
	t.Cleanup(func() { _ = unix.Close(pair[0]); _ = unix.Close(pair[1]) })
	if credentials {
		testutil.FailErr(t, "enable kernel sender credentials", guardianProtocolSocket(pair[1]))
	}
	return pair
}

func guardianMetadataWire(phase, status uint32) []byte {
	raw := make([]byte, 24)
	copy(raw, "PWGHAND1")
	binary.LittleEndian.PutUint32(raw[8:], 1)
	binary.LittleEndian.PutUint32(raw[12:], phase)
	binary.LittleEndian.PutUint32(raw[16:], status)
	return raw
}

func TestGuardianProtocolUsesKernelSenderIdentityAndBoundedStatus(t *testing.T) {
	caller := Identity{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}
	for _, notice := range []guardianNotice{{guardianReady, 0}, {guardianAdmit, 0}, {guardianSettled, 0}, {guardianSettled, 255}, {guardianFailed, 1}} {
		t.Run(fmt.Sprintf("phase%d status%d", notice.phase, notice.status), func(t *testing.T) {
			pair := guardianMetadataPair(t, true)
			testutil.FailErr(t, "send fixed guardian phase", sendGuardianMessage(pair[0], notice.phase, notice.status))
			got, err := receiveGuardianNotice(pair[1], os.Getpid(), caller)
			testutil.FailErr(t, "authenticate actual kernel credentials", err)
			if got != notice {
				t.Fatalf("guardian notice %+v, want %+v", got, notice)
			}
		})
	}
	for name, expected := range map[string]struct {
		pid      int
		identity Identity
	}{
		"other process": {os.Getpid() + 1, caller},
		"other user":    {os.Getpid(), Identity{UID: caller.UID ^ 1, GID: caller.GID}},
		"other group":   {os.Getpid(), Identity{UID: caller.UID, GID: caller.GID ^ 1}},
	} {
		t.Run(name, func(t *testing.T) {
			pair := guardianMetadataPair(t, true)
			testutil.FailErr(t, "send real sender credentials", sendGuardianMessage(pair[0], guardianReady, 0))
			if _, err := receiveGuardianNotice(pair[1], expected.pid, expected.identity); err == nil {
				t.Fatal("accepted readiness from a different selected process or identity")
			}
		})
	}
	for _, notice := range []guardianNotice{{0, 0}, {guardianFailed + 1, 0}, {guardianReady, 1}, {guardianAdmit, 1}, {guardianFailed, 0}, {guardianFailed, 2}, {guardianSettled, 256}} {
		pair := guardianMetadataPair(t, true)
		if sendGuardianMessage(pair[0], notice.phase, notice.status) == nil {
			t.Fatalf("sender accepted invalid notice %+v", notice)
		}
		raw := guardianMetadataWire(notice.phase, notice.status)
		_, err := unix.SendmsgN(pair[0], raw, nil, nil, unix.MSG_NOSIGNAL)
		testutil.FailErr(t, "inject malformed phase independently of sender guard", err)
		if _, err := receiveGuardianNotice(pair[1], os.Getpid(), caller); err == nil {
			t.Fatalf("receiver accepted invalid notice %+v", notice)
		}
	}
}

func TestGuardianProtocolRejectsNoncanonicalFramesAndMissingCredentials(t *testing.T) {
	caller := Identity{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}
	valid := guardianMetadataWire(guardianReady, 0)
	for name, change := range map[string]func([]byte) []byte{
		"magic":           func(b []byte) []byte { b[0] ^= 1; return b },
		"version":         func(b []byte) []byte { b[8] = 2; return b },
		"reserved":        func(b []byte) []byte { b[20] = 1; return b },
		"truncated":       func(b []byte) []byte { return b[:23] },
		"trailing":        func(b []byte) []byte { return append(b, 0) },
		"oversize packet": func(b []byte) []byte { return append(b, make([]byte, 128)...) },
	} {
		t.Run(name, func(t *testing.T) {
			pair := guardianMetadataPair(t, true)
			_, err := unix.SendmsgN(pair[0], change(append([]byte(nil), valid...)), nil, nil, unix.MSG_NOSIGNAL)
			testutil.FailErr(t, "inject noncanonical wire frame", err)
			if _, err := receiveGuardianNotice(pair[1], os.Getpid(), caller); err == nil {
				t.Fatal("accepted noncanonical guardian phase")
			}
		})
	}
	pair := guardianMetadataPair(t, false)
	testutil.FailErr(t, "send without credential transport", sendGuardianMessage(pair[0], guardianReady, 0))
	if _, err := receiveGuardianNotice(pair[1], os.Getpid(), caller); err == nil {
		t.Fatal("accepted readiness without kernel-generated sender credentials")
	}
}

func TestGuardianProtocolClosesInjectedDescriptorAuthority(t *testing.T) {
	for _, credentials := range []bool{false, true} {
		t.Run(fmt.Sprintf("credentials=%v", credentials), func(t *testing.T) {
			pair := guardianMetadataPair(t, credentials)
			pipe := make([]int, 2)
			testutil.FailErr(t, "create observable descriptor lifetime", unix.Pipe2(pipe, unix.O_CLOEXEC|unix.O_NONBLOCK))
			defer func() { _ = unix.Close(pipe[0]) }()
			_, err := unix.SendmsgN(pair[0], guardianMetadataWire(guardianReady, 0), unix.UnixRights(pipe[1]), nil, unix.MSG_NOSIGNAL)
			_ = unix.Close(pipe[1])
			testutil.FailErr(t, "inject descriptor with guardian notice", err)
			caller := Identity{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}
			if _, err := receiveGuardianNotice(pair[1], os.Getpid(), caller); err == nil {
				t.Fatal("guardian readiness accepted transferred descriptor authority")
			}
			var one [1]byte
			n, err := unix.Read(pipe[0], one[:])
			if err != nil || n != 0 {
				t.Fatalf("rejected descriptor remains open instead of pipe EOF: n=%d err=%v", n, err)
			}
		})
	}
}

func TestGuardianProtocolDistinguishesClosedPeerFromEmptyCredentialedPacket(t *testing.T) {
	caller := Identity{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}
	t.Run("closed peer", func(t *testing.T) {
		pair := guardianMetadataPair(t, true)
		testutil.FailErr(t, "close only owned peer write direction", unix.Shutdown(pair[0], unix.SHUT_WR))
		raw := make([]byte, 25)
		control := make([]byte, unix.CmsgSpace(unix.SizeofUcred))
		n, oobn, flags, _, err := unix.Recvmsg(pair[1], raw, control, unix.MSG_CMSG_CLOEXEC|unix.MSG_DONTWAIT)
		testutil.FailErr(t, "observe actual closed seqpacket", err)
		t.Logf("closed seqpacket scalar facts: bytes=%d ancillary_bytes=%d flags=%d", n, oobn, flags)
		if n != 0 || oobn != 0 || (flags != 0 && flags != unix.MSG_CMSG_CLOEXEC) {
			t.Fatal("closed peer returned an unexpected EOF tuple")
		}
		if _, err := receiveGuardianNotice(pair[1], os.Getpid(), caller); !errors.Is(err, io.EOF) {
			t.Fatalf("closed peer lost exact typed fatal EOF: %v", err)
		}
	})
	t.Run("empty credentialed packet", func(t *testing.T) {
		pair := guardianMetadataPair(t, true)
		send := func() {
			t.Helper()
			_, err := unix.SendmsgN(pair[0], nil, nil, nil, unix.MSG_NOSIGNAL)
			testutil.FailErr(t, "send owned zero-length packet", err)
		}
		send()
		raw := make([]byte, 25)
		control := make([]byte, unix.CmsgSpace(unix.SizeofUcred))
		n, oobn, flags, _, err := unix.Recvmsg(pair[1], raw, control, unix.MSG_CMSG_CLOEXEC|unix.MSG_DONTWAIT)
		testutil.FailErr(t, "observe empty credentialed packet", err)
		messages, err := unix.ParseSocketControlMessage(control[:oobn])
		testutil.FailErr(t, "decode actual zero-packet ancillary", err)
		t.Logf("empty credentialed seqpacket scalar facts: bytes=%d ancillary_bytes=%d flags=%d messages=%d", n, oobn, flags, len(messages))
		if n != 0 || len(messages) != 1 || messages[0].Header.Type != unix.SCM_CREDENTIALS {
			t.Fatal("zero-length packet lost kernel credentials")
		}
		send()
		if _, err := receiveGuardianNotice(pair[1], os.Getpid(), caller); err == nil || errors.Is(err, io.EOF) {
			t.Fatalf("empty authenticated packet gained admission or EOF meaning: %v", err)
		}
	})
}
