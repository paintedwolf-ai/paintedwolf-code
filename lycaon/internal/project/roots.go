package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/projectsource"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
)

// SourceID binds source effects to the durable project identity.
func (p *Project) SourceID() string {
	if p == nil {
		return ""
	}
	return p.ID
}

// SourceRoots exposes only attached filesystem facts to source operations.
func (p *Project) SourceRoots() []projectroot.RootRef {
	return RootRefsFrom(p)
}

// RootPaths returns unique project root paths, primary first.
func RootPaths(p *Project) []string {
	if p == nil || len(p.Roots) == 0 {
		return nil
	}
	out := make([]string, 0, len(p.Roots))
	seen := make(map[string]struct{}, len(p.Roots))
	appendRoot := func(r Root) {
		path := strings.TrimSpace(r.Path)
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	for _, r := range p.Roots {
		if r.IsPrimary {
			appendRoot(r)
		}
	}
	for _, r := range p.Roots {
		if !r.IsPrimary {
			appendRoot(r)
		}
	}
	return out
}

// RootRefsFrom converts persisted project roots to path-resolution refs (primary first).
func RootRefsFrom(p *Project) []projectroot.RootRef {
	if p == nil || len(p.Roots) == 0 {
		return nil
	}
	out := make([]projectroot.RootRef, 0, len(p.Roots))
	for _, r := range p.Roots {
		if r.IsPrimary {
			out = append(out, rootRefFrom(r))
		}
	}
	for _, r := range p.Roots {
		if !r.IsPrimary {
			out = append(out, rootRefFrom(r))
		}
	}
	return out
}

func rootRefFrom(r Root) projectroot.RootRef {
	return projectroot.RootRef{
		ID:        r.ID,
		Label:     r.Label,
		Path:      r.Path,
		IsPrimary: r.IsPrimary,
	}
}

// ResolveExistingDir returns the absolute path when path exists and is a directory.
func ResolveExistingDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", ErrInvalidPath
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidPath, err)
	}
	abs = filepath.Clean(abs)

	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", ErrPathNotFound, abs)
		}
		return "", err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", ErrPathNotFound, resolved)
		}
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s", ErrNotDirectory, resolved)
	}
	if refused, code := confine.AttachedWriteRootRefused(resolved); refused {
		return "", &RootRefusedError{Code: code, Path: resolved}
	}
	return resolved, nil
}

// RootRefusedError reports a refused attached root.
type RootRefusedError struct {
	Code string
	Path string
}

func (e *RootRefusedError) Error() string {
	if e == nil {
		return ErrRootRefused.Error()
	}
	if e.Code == "" {
		return fmt.Sprintf("%s: %s", ErrRootRefused.Error(), e.Path)
	}
	return fmt.Sprintf("%s: %s", ErrRootRefused.Error(), e.Code)
}

func (e *RootRefusedError) Unwrap() error { return ErrRootRefused }

// RootRefusedCode extracts the machine code from an ErrRootRefused chain.
func RootRefusedCode(err error) (string, bool) {
	var refused *RootRefusedError
	if errors.As(err, &refused) && refused.Code != "" {
		return refused.Code, true
	}
	return "", false
}

const gitRemoteHashTimeout = 3 * time.Second

// gitRemoteHash identifies origin's repository under a bounded timeout, so ssh and https
// spellings of one repository hash alike.
func gitRemoteHash(ctx context.Context, dir string) string {
	ctx, cancel := context.WithTimeout(ctx, gitRemoteHashTimeout)
	defer cancel()
	out, code, err := gitexec.Run(ctx, dir, []string{"remote", "get-url", "origin"}, gitexec.Opts{
		Profile: gitexec.ProfileHermetic,
		Timeout: gitRemoteHashTimeout,
	})
	if err != nil || code != 0 {
		return ""
	}
	url := strings.TrimSpace(string(out))
	if repo := projectsource.SourceRemoteRepo(url); repo != "" {
		url = repo
	}
	if url == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:])
}
