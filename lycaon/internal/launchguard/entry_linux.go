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

	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"golang.org/x/sys/unix"
)

// Descriptors the privileged stage hands the unprivileged entry.
const (
	entryImageFD    = 3
	entrySpecFD     = 4
	entryOriginFD   = 5
	entryBrokerFD   = 6
	entryLifetimeFD = 7
	maxEntryBytes   = 128 << 10
	entrySeals      = unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL
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
	origin := os.NewFile(entryOriginFD, "original-user-namespace")
	defer func() { _ = origin.Close() }()
	frame := os.NewFile(entrySpecFD, "sealed-entry-spec")
	defer func() { _ = frame.Close() }()
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
	image := os.NewFile(entryImageFD, "selected-desktop-image")
	defer func() { _ = image.Close() }()
	if err := rootRegular(image); err != nil {
		return err
	}
	if err := pkgmanifest.VerifyOpened(image, spec.Image); err != nil {
		return err
	}
	if err := verifyBroker(spec); err != nil {
		return err
	}
	lifetime := os.NewFile(entryLifetimeFD, "generation-lifetime")
	if err := rootRegular(lifetime); err != nil {
		return err
	}
	// Bootstrap descriptors must not reach the desktop. The broker and the
	// generation's lifetime lock stay open for it to claim.
	if err := unix.CloseRange(entryImageFD+1, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil {
		return err
	}
	for _, fd := range []int{entryBrokerFD, entryLifetimeFD} {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0); err != nil {
			return err
		}
	}
	unix.CloseOnExec(entryImageFD)
	// The execution domain belongs to this thread, which executes the desktop.
	runtime.LockOSThread()
	if err := restrictExecution(spec.Generation, image); err != nil {
		return err
	}
	args := append([]string{filepath.Join(spec.Generation.LogicalInstallRoot, "bin/painted-wolf-code")}, spec.Arguments...)
	return syscall.Exec("/proc/self/fd/3", args, spec.Environment)
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
		if uint32(group) != spec.Groups[i] {
			return errors.New("entry supplementary groups differ from the original caller")
		}
	}
	return nil
}

// verifyBroker requires the broker descriptor to be a seqpacket socket whose
// peer is the supervisor named by the sealed frame. The kernel records a
// socket pair's credentials when it is created, which the bootstrap did while
// still privileged, so the peer reads as uid 0 even after the drop.
func verifyBroker(spec EntrySpec) error {
	peer, err := unix.GetsockoptUcred(entryBrokerFD, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || peer.Uid != 0 || int(peer.Pid) != spec.SupervisorPID {
		return errors.New("desktop broker is not the launch supervisor")
	}
	kind, err := unix.GetsockoptInt(entryBrokerFD, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil || kind != unix.SOCK_SEQPACKET {
		return errors.New("invalid desktop broker transport")
	}
	return nil
}

func readEntry(file *os.File) (EntrySpec, error) {
	if err := rootRegular(file); err != nil {
		return EntrySpec{}, err
	}
	seals, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || seals&entrySeals != entrySeals {
		return EntrySpec{}, errors.New("entry specification is not sealed")
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
	if !ok || facts.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("%s is not a root-controlled regular object", file.Name())
	}
	return nil
}
