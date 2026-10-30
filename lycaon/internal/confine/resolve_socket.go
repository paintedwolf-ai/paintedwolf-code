package confine

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/socketref"

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
// An empty binding is a freshly resolved grant that has not been captured yet: it
// revalidates by pathname alone. An explicit binding revalidates through its own
// realization — a retained object must still be acquirable, a pathname rule must
// still resolve — and never falls back from one to the other.
func RevalidateSocketGrant(g SocketGrant) error {
	adapter := socketref.Platform()
	switch g.Binding.Enforcement {
	case "":
		if adapter.Command == socketref.NoRealization {
			return &SocketResolveError{Code: SocketResolveRefused, Path: g.ApprovedPath, Err: socketref.ErrUnsupported}
		}
	case socketref.KernelPathname:
		if g.Binding.Mode() != socketref.KernelPathname {
			return &SocketResolveError{Code: SocketResolveRefused, Path: g.ApprovedPath, Err: socketref.ErrUnsupported}
		}
	case socketref.RetainedObject:
		lease, err := g.Binding.Acquire()
		if err != nil {
			return &SocketResolveError{Code: SocketResolveChanged, Path: g.ApprovedPath, Err: err}
		}
		lease.Close()
	default:
		return &SocketResolveError{Code: SocketResolveRefused, Path: g.ApprovedPath, Err: socketref.ErrUnsupported}
	}
	// Canonicalization alone cannot prove the socket still exists.
	now := fspath.CanonicalPath(g.ApprovedPath)
	if now != g.ResolvedPath {
		return &SocketResolveError{Code: SocketResolveChanged, Path: g.ApprovedPath}
	}
	fi, err := os.Lstat(now)
	if err != nil || fi.Mode()&os.ModeSocket == 0 {
		return &SocketResolveError{Code: SocketResolveChanged, Path: g.ApprovedPath, Err: err}
	}
	if g.Binding.Enforcement == socketref.RetainedObject && !g.Binding.MatchesPath(now) {
		return &SocketResolveError{Code: SocketResolveChanged, Path: g.ApprovedPath, Err: socketref.ErrUnbound}
	}
	return nil
}

// CaptureSocketGrant retains the destination for an invocation before review.
// Inspection alone, including settings reads, never creates a reference.
func CaptureSocketGrant(ctx context.Context, g SocketGrant) (SocketGrant, error) {
	if g.Binding.Mode() == socketref.NoRealization {
		return SocketGrant{}, &SocketResolveError{Code: SocketResolveRefused, Path: g.ApprovedPath, Err: socketref.ErrUnsupported}
	}
	if g.Binding.Mode() == socketref.KernelPathname {
		return g, nil
	}
	if g.Binding.Mode() != socketref.RetainedObject {
		return SocketGrant{}, &SocketResolveError{Code: SocketResolveRefused, Path: g.ApprovedPath, Err: socketref.ErrUnbound}
	}
	binding, err := socketref.CaptureForInvocation(ctx, g.ApprovedPath)
	if err != nil {
		return SocketGrant{}, &SocketResolveError{Code: SocketResolveRefused, Path: g.ApprovedPath, Err: err}
	}
	resolved, err := binding.Path()
	if err != nil || resolved != g.ResolvedPath {
		return SocketGrant{}, &SocketResolveError{Code: SocketResolveChanged, Path: g.ApprovedPath, Err: err}
	}
	g.Binding = binding
	return g, nil
}

func validateSocketRealization(grants []SocketGrant, mode socketref.Enforcement) error {
	for _, grant := range grants {
		if grant.Binding.Mode() != mode {
			return &SocketResolveError{Code: SocketResolveRefused, Path: grant.ApprovedPath, Err: socketref.ErrUnbound}
		}
	}
	return nil
}
