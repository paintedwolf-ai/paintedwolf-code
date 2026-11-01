package confine

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/fspath"

	wire "github.com/lycaon/lycaon/pkg/api"
)

// SocketListenerIdentity records the listener credentials and socket identity
// captured when an exact AF_UNIX socket grant is approved.
type SocketListenerIdentity struct {
	UID         uint32
	PID         int32
	PIDVersion  uint32
	SigningID   string
	TeamID      string
	CDHash      string
	Dev         uint64
	Ino         uint64
	BoundPath   string
	ListenerPCB uint64
}

// IsZero reports whether no listener identity has been recorded.
func (id SocketListenerIdentity) IsZero() bool {
	return id.Ino == 0 && id.SigningID == "" && id.CDHash == "" && id.PID == 0
}

// ErrSocketListenerChanged indicates the destination socket listener no longer
// matches the approved listener identity.
var ErrSocketListenerChanged = errors.New("socket listener identity changed")

var (
	socketPinMu            sync.Mutex
	pinnedSocketListeners  = map[string]SocketListenerIdentity{}
	lastSocketRelayRefusal = map[string]*SocketResolveError{}
)

func socketGrantPinKey(approved, resolved string) string {
	return strings.TrimSpace(approved) + "\x00" + strings.TrimSpace(resolved)
}

func storePinnedSocketListener(approved, resolved string, id SocketListenerIdentity) {
	socketPinMu.Lock()
	pinnedSocketListeners[socketGrantPinKey(approved, resolved)] = id
	delete(lastSocketRelayRefusal, strings.TrimSpace(approved))
	delete(lastSocketRelayRefusal, strings.TrimSpace(resolved))
	socketPinMu.Unlock()
}

func lookupPinnedSocketListener(approved, resolved string) SocketListenerIdentity {
	socketPinMu.Lock()
	defer socketPinMu.Unlock()
	return pinnedSocketListeners[socketGrantPinKey(approved, resolved)]
}

func effectiveSocketListener(g SocketGrant) SocketListenerIdentity {
	if !g.Listener.IsZero() {
		return g.Listener
	}
	return lookupPinnedSocketListener(g.ApprovedPath, g.ResolvedPath)
}

func recordSocketRelayRefusal(g SocketGrant, err *SocketResolveError) {
	socketPinMu.Lock()
	if a := strings.TrimSpace(g.ApprovedPath); a != "" {
		lastSocketRelayRefusal[a] = err
	}
	if r := strings.TrimSpace(g.ResolvedPath); r != "" {
		lastSocketRelayRefusal[r] = err
	}
	socketPinMu.Unlock()
}

// SocketRelayRefusal returns the most recent structured refusal recorded by an
// engine-owned socket relay for any grant in c.
func SocketRelayRefusal(c Confinement) *SocketResolveError {
	socketPinMu.Lock()
	defer socketPinMu.Unlock()
	for _, g := range c.SocketGrants {
		if err := lastSocketRelayRefusal[strings.TrimSpace(g.ApprovedPath)]; err != nil {
			return err
		}
	}
	return nil
}

// SocketResolveError is a structured failure resolving a model-requested socket path.
type SocketResolveError struct {
	Code wire.ApiErrorCode
	Path string
	Err  error
}

func (e *SocketResolveError) Error() string {
	if e == nil {
		return "confine: socket resolve error"
	}
	if e.Err != nil {
		return fmt.Sprintf("confine: %s: %s: %v", e.Code, e.Path, e.Err)
	}
	return fmt.Sprintf("confine: %s: %s", e.Code, e.Path)
}

func (e *SocketResolveError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Stable reject codes for ResolveSocketRequest (mapped to policy units by tools).
const (
	SocketResolveInvalid   wire.ApiErrorCode = "sandbox_socket_path_invalid"
	SocketResolveNotFound  wire.ApiErrorCode = "sandbox_socket_path_not_found"
	SocketResolveNotSocket wire.ApiErrorCode = "sandbox_socket_path_not_socket"
	SocketResolveRefused   wire.ApiErrorCode = "sandbox_socket_path_refused"
	SocketResolveLimit     wire.ApiErrorCode = "sandbox_socket_path_limit"
	SocketResolveChanged   wire.ApiErrorCode = "sandbox_socket_path_changed"
)

// ResolveSocketRequest validates and resolves one requested socket path.
func ResolveSocketRequest(path string) (SocketGrant, error) {
	if reason := socketPathSyntaxReason(path); reason != "" {
		return SocketGrant{}, &SocketResolveError{Code: SocketResolveInvalid, Path: path}
	}
	_, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return SocketGrant{}, &SocketResolveError{Code: SocketResolveNotFound, Path: path, Err: err}
		}
		return SocketGrant{}, &SocketResolveError{Code: SocketResolveRefused, Path: path, Err: err}
	}
	resolved := fspath.CanonicalPath(path)
	if resolved == "" {
		return SocketGrant{}, &SocketResolveError{Code: SocketResolveInvalid, Path: path}
	}
	if reason := socketPathSyntaxReason(resolved); reason != "" {
		return SocketGrant{}, &SocketResolveError{Code: SocketResolveInvalid, Path: path}
	}
	fi, err := os.Lstat(resolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return SocketGrant{}, &SocketResolveError{Code: SocketResolveNotFound, Path: path, Err: err}
		}
		return SocketGrant{}, &SocketResolveError{Code: SocketResolveRefused, Path: path, Err: err}
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return SocketGrant{}, &SocketResolveError{Code: SocketResolveNotSocket, Path: path}
	}
	if socketHardLinked(fi) {
		return SocketGrant{}, &SocketResolveError{Code: SocketResolveRefused, Path: path}
	}
	listener, err := inspectSocketListener(path, resolved, fi)
	if err != nil {
		return SocketGrant{}, &SocketResolveError{Code: SocketResolveRefused, Path: path, Err: err}
	}
	storePinnedSocketListener(path, resolved, listener)
	return SocketGrant{ApprovedPath: path, ResolvedPath: resolved, Listener: listener}, nil
}

// RevalidateSocketGrant re-resolves ApprovedPath and requires it still equal
// ResolvedPath and match the approved listener identity.
func RevalidateSocketGrant(g SocketGrant) error {
	// Canonicalization alone cannot prove the socket still exists.
	now := fspath.CanonicalPath(g.ApprovedPath)
	if now != g.ResolvedPath {
		return &SocketResolveError{Code: SocketResolveChanged, Path: g.ApprovedPath}
	}
	if g.ApprovedPath == g.ResolvedPath {
		if afi, err := os.Lstat(g.ApprovedPath); err != nil || afi.Mode()&os.ModeSymlink != 0 {
			return &SocketResolveError{Code: SocketResolveChanged, Path: g.ApprovedPath, Err: err}
		}
	}
	fi, err := os.Lstat(now)
	if err != nil || fi.Mode()&os.ModeSocket == 0 || socketHardLinked(fi) {
		return &SocketResolveError{Code: SocketResolveChanged, Path: g.ApprovedPath, Err: err}
	}
	pinned := effectiveSocketListener(g)
	updated, err := revalidateSocketListener(g.ApprovedPath, g.ResolvedPath, fi, pinned)
	if err != nil {
		return &SocketResolveError{Code: SocketResolveChanged, Path: g.ApprovedPath, Err: err}
	}
	if !updated.IsZero() {
		storePinnedSocketListener(g.ApprovedPath, g.ResolvedPath, updated)
	}
	return nil
}
