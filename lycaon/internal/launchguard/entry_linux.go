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
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"golang.org/x/sys/unix"
)

const (
	entryImageFD      = 3
	entrySpecFD       = 4
	entryOriginFD     = 5
	entryBrokerFD     = 6
	entryGenerationFD = 11
	maxEntryBytes     = 128 << 10
	entrySeals        = unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL
)

// EntrySpec is carried only by a sealed, root-created descriptor. It never
// derives installation authority from the command line or environment.
type EntrySpec struct {
	Version     int                    `json:"version"`
	ParentPID   int                    `json:"parent_pid"`
	UID         uint32                 `json:"uid"`
	GID         uint32                 `json:"gid"`
	Groups      []uint32               `json:"groups"`
	Generation  GenerationPolicy       `json:"generation"`
	Image       pkgmanifest.Executable `json:"image"`
	Arguments   []string               `json:"arguments"`
	Environment []string               `json:"environment"`
	Broker      bool                   `json:"broker"`
}

// EnterDesktop is the unprivileged static entry's only operation. All
// executable and argument selection was completed by its privileged parent.
func EnterDesktop() error {
	origin := os.NewFile(entryOriginFD, "original-user-namespace")
	defer func() { _ = origin.Close() }()
	frame := os.NewFile(entrySpecFD, "sealed-entry-spec")
	defer func() { _ = frame.Close() }()
	spec, err := readEntry(frame)
	if err != nil {
		return err
	}
	if os.Getppid() != spec.ParentPID {
		return errors.New("entry caller or parent changed")
	}
	if err := prepareEntry(spec, origin); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(unix.SIGKILL), 0, 0, 0); err != nil {
		return err
	}
	if os.Getppid() != spec.ParentPID {
		return errors.New("entry parent exited")
	}
	image := os.NewFile(entryImageFD, "selected-desktop-image")
	defer func() { _ = image.Close() }()
	if err := rootRegular(image); err != nil {
		return err
	}
	if err := pkgmanifest.VerifyOpened(image, spec.Image); err != nil {
		return err
	}
	if spec.Broker {
		peer, err := unix.GetsockoptUcred(entryBrokerFD, unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil || peer.Uid != 0 || int(peer.Pid) != spec.ParentPID {
			return errors.New("desktop broker is not the privileged parent")
		}
		kind, err := unix.GetsockoptInt(entryBrokerFD, unix.SOL_SOCKET, unix.SO_TYPE)
		if err != nil || kind != unix.SOCK_SEQPACKET {
			return errors.New("invalid desktop broker transport")
		}
	}
	// Runtime and bootstrap descriptors must not reach application helpers.
	if err := unix.CloseRange(entryBrokerFD, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil {
		return err
	}
	if spec.Broker {
		if _, err := unix.FcntlInt(entryBrokerFD, unix.F_SETFD, 0); err != nil {
			return err
		}
	}
	unix.CloseOnExec(entryImageFD)
	unix.CloseOnExec(entrySpecFD)
	unix.CloseOnExec(entryOriginFD)
	args := append([]string{filepath.Join(spec.Generation.LogicalInstallRoot, "bin/painted-wolf-code")}, spec.Arguments...)
	return syscall.Exec("/proc/self/fd/3", args, spec.Environment)
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
	if spec.Version != 1 || spec.ParentPID <= 1 || spec.UID == 0 || spec.UID == ^uint32(0) || spec.GID == ^uint32(0) {
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
