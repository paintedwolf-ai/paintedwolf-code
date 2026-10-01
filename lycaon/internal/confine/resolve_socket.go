package confine

import (
	"errors"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/fspath"

	wire "github.com/lycaon/lycaon/pkg/api"
)

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
	return SocketGrant{ApprovedPath: path, ResolvedPath: resolved}, nil
}

// RevalidateSocketGrant re-resolves ApprovedPath and requires it still equal ResolvedPath.
func RevalidateSocketGrant(g SocketGrant) error {
	// Canonicalization alone cannot prove the socket still exists.
	now := fspath.CanonicalPath(g.ApprovedPath)
	if now != g.ResolvedPath {
		return &SocketResolveError{Code: SocketResolveChanged, Path: g.ApprovedPath}
	}
	fi, err := os.Lstat(now)
	if err != nil || fi.Mode()&os.ModeSocket == 0 {
		return &SocketResolveError{Code: SocketResolveChanged, Path: g.ApprovedPath, Err: err}
	}
	return nil
}
