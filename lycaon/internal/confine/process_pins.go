package confine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// LaunchPaths opens metadata handles under the initiating operation's current
// authority. It grants no source read, execution permission or path conversion.
type LaunchPaths interface {
	OpenExecutable(context.Context, string) (*os.File, error)
	OpenCwd(context.Context) (*os.File, error)
}

var ErrLaunchIdentityChanged = errors.New("selected launch identity changed")

// ProcessInputs holds the selected native entry and actual cwd across handoff.
// Kernel pathname lookup is not atomic with the final current-authority check;
// these facts do not assert immutable historical executable or interpreter bytes.
type ProcessInputs struct {
	paths   LaunchPaths
	limits  LaunchVectorLimits
	entry   launchEntry
	cwd     *os.File
	cwdInfo os.FileInfo
	mu      sync.Mutex
	closed  bool
	bound   bool
}
type launchEntry struct {
	path   string
	file   *os.File
	info   os.FileInfo
	change fileChange
}
type fileChange struct {
	Seconds, Nanoseconds int64
	UID, GID             uint32
}

func CaptureProcessInputs(ctx context.Context, path string, paths LaunchPaths, limits LaunchVectorLimits) (*ProcessInputs, error) {
	if ctx == nil || paths == nil || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("launch capture requires context, current path authority and resolved native entry")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := limits.validate(); err != nil {
		return nil, err
	}
	inputs := &ProcessInputs{paths: paths, limits: limits}
	accepted := false
	defer func() {
		if !accepted {
			_ = inputs.Close()
		}
	}()
	var err error
	inputs.cwd, err = paths.OpenCwd(ctx)
	if err != nil {
		return nil, err
	}
	if inputs.cwd == nil {
		return nil, fmt.Errorf("cwd authority returned no handle")
	}
	inputs.cwdInfo, err = inputs.cwd.Stat()
	if err != nil {
		return nil, err
	}
	if !inputs.cwdInfo.IsDir() {
		return nil, fmt.Errorf("launch cwd is not a directory")
	}
	file, err := paths.OpenExecutable(ctx, path)
	if err != nil {
		return nil, err
	}
	if file == nil {
		return nil, fmt.Errorf("executable authority returned no metadata handle")
	}
	inputs.entry = launchEntry{path: strings.Clone(path), file: file}
	inputs.entry.info, err = file.Stat()
	if err != nil {
		return nil, err
	}
	if !inputs.entry.info.Mode().IsRegular() {
		return nil, fmt.Errorf("selected executable is not a regular file")
	}
	inputs.entry.change, err = nativeFileChange(inputs.entry.info)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	accepted = true
	return inputs, nil
}

func sameImage(info os.FileInfo, change fileChange, current os.FileInfo) bool {
	observed, err := nativeFileChange(current)
	return err == nil && os.SameFile(info, current) && info.Mode() == current.Mode() && info.Size() == current.Size() && info.ModTime().Equal(current.ModTime()) && observed == change
}

func (p *ProcessInputs) revalidate(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrLaunchIdentityChanged
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cwd, err := p.paths.OpenCwd(ctx)
	if err != nil {
		return err
	}
	if cwd == nil {
		return ErrLaunchIdentityChanged
	}
	current, err := cwd.Stat()
	_ = cwd.Close()
	if err != nil {
		return err
	}
	if !os.SameFile(p.cwdInfo, current) {
		return ErrLaunchIdentityChanged
	}
	file, err := p.paths.OpenExecutable(ctx, p.entry.path)
	if err != nil {
		return err
	}
	if file == nil {
		return ErrLaunchIdentityChanged
	}
	current, err = file.Stat()
	_ = file.Close()
	if err != nil {
		return err
	}
	if !sameImage(p.entry.info, p.entry.change, current) {
		return ErrLaunchIdentityChanged
	}
	return nil
}

func (p *ProcessInputs) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	var err error
	if p.cwd != nil {
		err = p.cwd.Close()
	}
	if p.entry.file != nil {
		err = errors.Join(err, p.entry.file.Close())
	}
	p.paths, p.cwd, p.entry.file = nil, nil, nil
	return err
}

// NativeLaunchPaths retains ordinary native execution metadata authority for
// host-selected callers. It does not grant model read access. Portable callers
// provide the existing invocation path owner instead of deriving a read grant.
func NativeLaunchPaths(cwd string) LaunchPaths { return nativeLaunchPaths{cwd: cwd} }

type nativeLaunchPaths struct{ cwd string }

func (p nativeLaunchPaths) OpenExecutable(ctx context.Context, path string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return openNativeLaunchMetadata(path, false)
}
func (p nativeLaunchPaths) OpenCwd(ctx context.Context) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return openNativeLaunchMetadata(p.cwd, true)
}
