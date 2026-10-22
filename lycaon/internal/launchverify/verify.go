package launchverify

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
)

// Code names why a process failed verification. Codes are stable facts for
// callers; the detail is diagnostic prose.
type Code string

const (
	// CodeManifestUnreadable: the installed manifest cannot be read or lacks
	// the role, so nothing can be verified on this host.
	CodeManifestUnreadable Code = "manifest_unreadable"
	// CodeProcessGone: the process filesystem no longer describes the pid.
	CodeProcessGone Code = "process_gone"
	// CodeExecutableUnreadable: the running image could not be opened.
	CodeExecutableUnreadable Code = "executable_unreadable"
	// CodeExecutableMismatch: the running image is not the installed executable.
	CodeExecutableMismatch Code = "executable_mismatch"
	// CodeUIDMismatch: the process belongs to another user than claimed.
	CodeUIDMismatch Code = "uid_mismatch"
	// CodeNoParent: the process was not started by another process.
	CodeNoParent Code = "no_parent"
	// CodeProcessChanged: the pid or its parent changed during verification.
	CodeProcessChanged Code = "process_changed"
)

// Refusal is a structured verification failure.
type Refusal struct {
	Code   Code
	Role   string
	PID    int
	Detail string
}

func (r *Refusal) Error() string {
	if r.Detail == "" {
		return fmt.Sprintf("%s for pid %d: %s", r.Role, r.PID, r.Code)
	}
	return fmt.Sprintf("%s for pid %d: %s (%s)", r.Role, r.PID, r.Code, r.Detail)
}

func refuse(code Code, role string, pid int, detail string) error {
	return &Refusal{Code: code, Role: role, PID: pid, Detail: detail}
}

// CodeOf returns the refusal code err carries, or "" for other errors.
func CodeOf(err error) Code {
	var refusal *Refusal
	if errors.As(err, &refusal) {
		return refusal.Code
	}
	return ""
}

// Verifier checks processes under ProcRoot against Manifest.
type Verifier struct {
	ProcRoot string
	Manifest *pkgmanifest.Manifest
	// InstallRoot and StorePolicy come only from a root-controlled service policy.
	InstallRoot string
	StorePolicy *StorePolicy
	// statFile replaces the filesystem in tests of the mapping check.
	statFile func(string) (fileFacts, error)
}

// Chain is a verified engine and the desktop app that started it.
type Chain struct {
	Engine   Snapshot
	Launcher Snapshot
}

// VerifyChain proves that pid is the installed engine owned by uid and that
// its parent is the installed desktop app owned by the same uid, then checks
// that neither process changed while their images were read.
func (v Verifier) VerifyChain(pid, uid int) (Chain, error) {
	engine, err := v.snapshot(pid)
	if err != nil {
		return Chain{}, refuse(CodeProcessGone, RoleEngine, pid, err.Error())
	}
	if engine.UID != uid || engine.EUID != uid {
		return Chain{}, refuse(CodeUIDMismatch, RoleEngine, pid, fmt.Sprintf("uid %d/%d, caller %d", engine.UID, engine.EUID, uid))
	}
	if engine.PPID <= 1 {
		return Chain{}, refuse(CodeNoParent, RoleEngine, pid, "")
	}
	launcher, err := v.snapshot(engine.PPID)
	if err != nil {
		return Chain{}, refuse(CodeProcessGone, RoleDesktopApp, engine.PPID, err.Error())
	}
	if launcher.UID != uid || launcher.EUID != uid {
		return Chain{}, refuse(CodeUIDMismatch, RoleDesktopApp, launcher.PID, fmt.Sprintf("uid %d/%d, caller %d", launcher.UID, launcher.EUID, uid))
	}
	if err := v.VerifyExecutable(pid, RoleEngine); err != nil {
		return Chain{}, err
	}
	if err := v.VerifyExecutable(launcher.PID, RoleDesktopApp); err != nil {
		return Chain{}, err
	}
	if err := v.VerifyMappings(pid, RoleEngine); err != nil {
		return Chain{}, err
	}
	if err := v.VerifyMappings(launcher.PID, RoleDesktopApp); err != nil {
		return Chain{}, err
	}
	chain := Chain{Engine: engine, Launcher: launcher}
	if err := v.Recheck(chain); err != nil {
		return Chain{}, err
	}
	return chain, nil
}

// Recheck confirms that both processes of a verified chain are still the
// ones verified: a pid reused by another process has a new start time, and
// a parent that exited reparents the engine.
func (v Verifier) Recheck(chain Chain) error {
	engine, err := v.snapshot(chain.Engine.PID)
	if err != nil {
		return refuse(CodeProcessGone, RoleEngine, chain.Engine.PID, err.Error())
	}
	if !engine.Same(chain.Engine) {
		return refuse(CodeProcessChanged, RoleEngine, chain.Engine.PID, "")
	}
	launcher, err := v.snapshot(chain.Launcher.PID)
	if err != nil {
		return refuse(CodeProcessGone, RoleDesktopApp, chain.Launcher.PID, err.Error())
	}
	if !launcher.Same(chain.Launcher) {
		return refuse(CodeProcessChanged, RoleDesktopApp, chain.Launcher.PID, "")
	}
	return nil
}

// VerifyExecutable hashes the image pid is running, through the exe link
// opened as a descriptor, and compares it with the manifest's entry for the
// role's installed path.
func (v Verifier) VerifyExecutable(pid int, role string) error {
	if v.StorePolicy != nil && v.InstallRoot == "" {
		return refuse(CodeManifestUnreadable, role, pid, "a selected closure requires its exact installation root")
	}
	want, size, ok := expected(v.Manifest, role, v.InstallRoot)
	if !ok {
		return refuse(CodeManifestUnreadable, role, pid, "the manifest lists no such executable")
	}
	file, err := os.Open(v.procPath(pid, "exe"))
	if err != nil {
		return refuse(CodeExecutableUnreadable, role, pid, err.Error())
	}
	defer func() { _ = file.Close() }()
	if v.StorePolicy != nil {
		actual, err := file.Stat()
		installed, installedErr := os.Stat(rolePath(role, v.InstallRoot))
		if err != nil || installedErr != nil || !os.SameFile(actual, installed) {
			return refuse(CodeExecutableMismatch, role, pid, "the running image is not the selected generation's executable inode")
		}
	}
	hash := sha256.New()
	n, err := io.Copy(hash, file)
	if err != nil {
		return refuse(CodeExecutableUnreadable, role, pid, err.Error())
	}
	if n != size || subtle.ConstantTimeCompare(hash.Sum(nil), want) != 1 {
		return refuse(CodeExecutableMismatch, role, pid, "")
	}
	return nil
}
