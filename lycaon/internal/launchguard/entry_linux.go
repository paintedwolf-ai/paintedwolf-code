//go:build linux

package launchguard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/launchbroker"
	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"golang.org/x/sys/unix"
)

// Descriptors the privileged stage hands the unprivileged entry.
const (
	entryImageFD       = 3
	entrySpecFD        = 4
	entryOriginFD      = 5
	entryBrokerFD      = 6
	entryLifetimeFD    = 7
	protectedLaunchEnv = "PW_PROTECTED_LAUNCH"
	maxEntryBytes      = 128 << 10
	entrySeals         = unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL
)

// EntrySpec is carried only by a sealed, root-created descriptor. It never
// derives installation authority from the command line or environment.
type EntrySpec struct {
	Version int `json:"version"`
	// SupervisorPID is the bootstrap's unprivileged continuation, which holds
	// the broker's other end and reaps every descendant.
	SupervisorPID int                    `json:"supervisor_pid"`
	UID           uint32                 `json:"uid"`
	GID           uint32                 `json:"gid"`
	Groups        []uint32               `json:"groups"`
	Generation    GenerationPolicy       `json:"generation"`
	Image         pkgmanifest.Executable `json:"image"`
	Arguments     []string               `json:"arguments"`
	Environment   []string               `json:"environment"`
}

// EnterDesktop is the unprivileged entry's only operation. It starts as the
// login identity inside the protected namespace, with every selection already
// made by the privileged stage, and seals itself before executing the desktop.
func EnterDesktop() error {
	// Reject unrelated runtime descriptors before taking ownership of the inherited frame.
	if seals, err := unix.FcntlInt(entrySpecFD, unix.F_GET_SEALS, 0); err != nil || seals&entrySeals != entrySeals {
		return unsealedEntry(entrySpecFD, seals, err)
	}
	runtime.LockOSThread()
	return enterDesktop(
		os.NewFile(entryImageFD, "selected-desktop-image"),
		os.NewFile(entrySpecFD, "sealed-entry-spec"),
		os.NewFile(entryOriginFD, "original-user-namespace"),
		entryBrokerFD,
		os.NewFile(entryLifetimeFD, "generation-lifetime"),
		os.NewFile(entrySelfFD+1, "guardian-desktop-admission"),
	)
}

func enterDesktop(image, frame, origin *os.File, brokerFD int, lifetime, gate *os.File) error {
	// Keep the seal, verification, Landlock restriction and execve on one OS thread.
	runtime.LockOSThread()
	defer runtime.KeepAlive(lifetime)
	defer func() { _ = origin.Close() }()
	defer func() { _ = frame.Close() }()
	defer func() { _ = image.Close() }()
	spec, err := readEntry(frame)
	if err != nil {
		return err
	}
	if err := sameIdentity(spec); err != nil {
		return err
	}
	if err := SealEntry(origin); err != nil {
		return err
	}
	if err := rootRegular(image); err != nil {
		return err
	}
	if err := pkgmanifest.VerifyOpened(image, spec.Image); err != nil {
		return err
	}
	if err := verifyBroker(brokerFD, spec); err != nil {
		return err
	}
	if err := rootRegular(lifetime); err != nil {
		return err
	}
	// Validate caller-owned paths such as HOME only after privilege drop.
	env := append(DesktopEnvironment(spec.Environment, spec.UID), protectedLaunchEnv+"=1")
	// The desktop consumes the authenticated selection before starting threads.
	// Only its sealed frame, broker and generation lifetime lock survive exec.
	if err := unix.CloseRange(entryImageFD+1, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil {
		return err
	}
	for _, fd := range []int{int(frame.Fd()), brokerFD, int(lifetime.Fd())} {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0); err != nil {
			return err
		}
	}
	unix.CloseOnExec(int(image.Fd()))
	if err := restrictExecution(spec.Generation, image); err != nil {
		return err
	}
	if gate == nil {
		return errors.New("desktop lacks guardian admission")
	}
	var gateStat unix.Stat_t
	if err := unix.Fstat(int(gate.Fd()), &gateStat); err != nil || gateStat.Mode&unix.S_IFMT != unix.S_IFIFO || gateStat.Uid != 0 {
		return errors.New("desktop admission is not the root-created pipe")
	}
	marker, err := io.ReadAll(io.LimitReader(gate, 2))
	_ = gate.Close()
	if err != nil || len(marker) != 1 || marker[0] != 1 {
		return errors.New("guardian did not admit protected desktop")
	}
	args := append([]string{filepath.Join(spec.Generation.LogicalInstallRoot, "bin/painted-wolf-code")}, spec.Arguments...)
	// #nosec G204 -- fd 3 is the verified generation image; arguments remain separate from the program.
	return syscall.Exec("/proc/self/fd/3", args, env)
}

// sameIdentity requires the clone's identity change to have produced exactly
// the original login identity on every credential.
func sameIdentity(spec EntrySpec) error {
	uid, euid, suid := unix.Getresuid()
	gid, egid, sgid := unix.Getresgid()
	for _, id := range []int{uid, euid, suid} {
		if id != int(spec.UID) {
			return errors.New("entry user identity differs from the original caller")
		}
	}
	for _, id := range []int{gid, egid, sgid} {
		if id != int(spec.GID) {
			return errors.New("entry group identity differs from the original caller")
		}
	}
	groups, err := unix.Getgroups()
	if err != nil {
		return err
	}
	if len(groups) != len(spec.Groups) {
		return errors.New("entry supplementary groups differ from the original caller")
	}
	for i, group := range groups {
		if group < 0 || uint64(group) > uint64(^uint32(0)) || uint32(group) != spec.Groups[i] {
			return errors.New("entry supplementary groups differ from the original caller")
		}
	}
	return nil
}

// verifyBroker requires the broker descriptor to be a seqpacket socket whose
// peer is the supervisor named by the sealed frame. The kernel records a
// socket pair's credentials when it is created, which the bootstrap did while
// still privileged, so the peer reads as uid 0 even after the drop.
func verifyBroker(brokerFD int, spec EntrySpec) error {
	peer, err := launchbroker.CheckSocket(brokerFD)
	if err != nil || peer.Uid != trustedOwner || int(peer.Pid) != spec.SupervisorPID {
		return errors.New("desktop broker is not the launch supervisor")
	}
	return nil
}

func readEntry(file *os.File) (EntrySpec, error) {
	if err := rootRegular(file); err != nil {
		return EntrySpec{}, err
	}
	seals, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || seals&entrySeals != entrySeals {
		return EntrySpec{}, unsealedEntry(file.Fd(), seals, err)
	}
	raw, err := io.ReadAll(io.NewSectionReader(file, 0, maxEntryBytes+1))
	if err != nil {
		return EntrySpec{}, err
	}
	if len(raw) > maxEntryBytes {
		return EntrySpec{}, errors.New("entry specification exceeds its bound")
	}
	return decodeEntry(raw)
}

// unsealedEntry refuses a descriptor that does not carry the sealed frame and
// names what sat at that number, so a refusal inside a supervised launch can
// be traced to the handoff that produced it. The facts are diagnostic only.
func unsealedEntry(fd uintptr, seals int, err error) error {
	target, linkErr := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
	if linkErr != nil {
		target = "unreadable link: " + linkErr.Error()
	}
	identity := "fstat failed"
	var info unix.Stat_t
	if statErr := unix.Fstat(int(fd), &info); statErr == nil {
		identity = fmt.Sprintf("mode %#o dev %d inode %d size %d", info.Mode, info.Dev, info.Ino, info.Size)
	}
	return fmt.Errorf("entry specification is not sealed: descriptor %d is %s (%s) with seals %#x: %w", fd, target, identity, seals, err)
}

func decodeEntry(raw []byte) (EntrySpec, error) {
	var spec EntrySpec
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return spec, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return spec, errors.New("entry specification has trailing data")
	}
	if spec.Version != 1 || spec.SupervisorPID <= 1 || spec.UID == 0 || spec.UID == ^uint32(0) || spec.GID == ^uint32(0) {
		return spec, errors.New("invalid entry identity")
	}
	if !filepath.IsAbs(spec.Image.Path) || filepath.Clean(spec.Image.Path) != spec.Image.Path || filepath.Base(spec.Image.Path) != "painted-wolf-code" {
		return spec, errors.New("entry does not name the selected desktop role")
	}
	policyRaw, err := json.Marshal(spec.Generation)
	if err != nil {
		return spec, err
	}
	if _, err := decodePolicy(policyRaw); err != nil {
		return spec, err
	}
	if spec.Image.Path != filepath.Join(spec.Generation.GenerationRoot, "bin/painted-wolf-code") {
		return spec, errors.New("entry image differs from selected generation")
	}
	for _, group := range spec.Groups {
		if group == ^uint32(0) {
			return spec, errors.New("invalid supplementary group")
		}
	}
	seen := map[string]bool{}
	for _, value := range spec.Environment {
		key, _, ok := strings.Cut(value, "=")
		if !ok || key == "" || strings.ContainsRune(value, 0) || seen[key] {
			return spec, errors.New("invalid entry environment")
		}
		seen[key] = true
	}
	for _, arg := range spec.Arguments {
		if strings.ContainsRune(arg, 0) {
			return spec, errors.New("invalid entry argument")
		}
	}
	return spec, nil
}

func rootRegular(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	facts, ok := info.Sys().(*syscall.Stat_t)
	if !ok || facts.Uid != trustedOwner || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("%s is not a root-controlled regular object", file.Name())
	}
	return nil
}
