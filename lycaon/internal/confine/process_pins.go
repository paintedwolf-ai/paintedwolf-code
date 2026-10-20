package confine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// LaunchPaths opens current authority-checked execution inputs. Each successful
// open transfers a new handle to the launch operation; no grant is inferred
// from an absolute spelling or from an already held descriptor.
type LaunchPaths interface {
	OpenExecutable(context.Context, string) (*os.File, error)
	OpenCwd(context.Context) (*os.File, error)
}

// ErrLaunchIdentityChanged refuses a changed selection before user code release.
var ErrLaunchIdentityChanged = errors.New("selected launch identity changed")

// ProcessPins retains the selected entry/interpreter identities and real cwd.
// Close it after the prepared process has settled. Capture/release checks do not
// promise immutable files/plugins an interpreter opens after process release.
type ProcessPins struct {
	paths   LaunchPaths
	limits  LaunchVectorLimits
	images  []pinnedImage
	words   [][]string
	cwd     *os.File
	cwdInfo os.FileInfo
	mu      sync.Mutex
	closed  bool
	bound   bool
}

type pinnedImage struct {
	path   string
	file   *os.File
	info   os.FileInfo
	change fileChange
}

// CaptureProcessPins freezes a previously resolved native launch entry. The
// supplied path owner must apply current invocation authority on every reopen.
func CaptureProcessPins(ctx context.Context, nativePath string, paths LaunchPaths, limits LaunchVectorLimits) (*ProcessPins, error) {
	if ctx == nil || paths == nil || !filepath.IsAbs(nativePath) {
		return nil, fmt.Errorf("launch capture requires context, path admission and a resolved native entry")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := limits.validate(); err != nil {
		return nil, err
	}
	p := &ProcessPins{paths: paths, limits: limits}
	accepted := false
	defer func() {
		if !accepted {
			_ = p.Close()
		}
	}()
	var err error
	p.cwd, err = paths.OpenCwd(ctx)
	if err != nil {
		return nil, err
	}
	if p.cwd == nil {
		return nil, fmt.Errorf("cwd admission returned no handle")
	}
	p.cwdInfo, err = p.cwd.Stat()
	if err != nil {
		return nil, err
	}
	if !p.cwdInfo.IsDir() {
		return nil, fmt.Errorf("launch cwd is not a directory")
	}
	next := nativePath
	for depth := 0; depth < maximumInterpreterDepth; depth++ {
		image, header, err := captureImage(ctx, paths, next)
		if err != nil {
			return nil, err
		}
		p.images = append(p.images, image)
		words, err := platformShebang(header)
		if err != nil {
			return nil, err
		}
		if len(words) == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			accepted = true
			return p, nil
		}
		p.words = append(p.words, words)
		next = words[0]
	}
	return nil, fmt.Errorf("interpreter depth exceeds platform launch bound")
}

func captureImage(ctx context.Context, paths LaunchPaths, path string) (pinnedImage, []byte, error) {
	file, err := paths.OpenExecutable(ctx, path)
	if err != nil {
		return pinnedImage{}, nil, err
	}
	if file == nil {
		return pinnedImage{}, nil, fmt.Errorf("executable admission returned no handle")
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return pinnedImage{}, nil, err
	}
	if !info.Mode().IsRegular() {
		return pinnedImage{}, nil, fmt.Errorf("selected executable is not a regular file")
	}
	change, err := nativeFileChange(info)
	if err != nil {
		return pinnedImage{}, nil, err
	}
	header := make([]byte, platformShebangBytes)
	n, err := file.ReadAt(header, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return pinnedImage{}, nil, err
	}
	current, err := file.Stat()
	if err != nil {
		return pinnedImage{}, nil, err
	}
	if !sameImage(info, change, current) {
		return pinnedImage{}, nil, ErrLaunchIdentityChanged
	}
	accepted = true
	return pinnedImage{path: strings.Clone(path), file: file, info: info, change: change}, header[:n], nil
}

func sameImage(info os.FileInfo, change fileChange, current os.FileInfo) bool {
	observed, err := nativeFileChange(current)
	return err == nil && os.SameFile(info, current) && info.Mode() == current.Mode() && info.Size() == current.Size() && info.ModTime().Equal(current.ModTime()) && observed == change
}

func (p *ProcessPins) revalidate(ctx context.Context, requested ...string) error {
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
	info, err := cwd.Stat()
	_ = cwd.Close()
	if err != nil {
		return err
	}
	if !os.SameFile(info, p.cwdInfo) {
		return ErrLaunchIdentityChanged
	}
	images := p.images
	// A slash-bearing invocation retains its exact operand spelling. Admit that
	// spelling too; identity equality alone cannot grant a second pathname.
	if len(requested) == 1 && strings.ContainsAny(requested[0], "/\\") {
		image := p.images[0]
		image.path = requested[0]
		images = append(append([]pinnedImage(nil), images...), image)
	}
	for _, image := range images {
		current, err := p.paths.OpenExecutable(ctx, image.path)
		if err != nil {
			return err
		}
		if current == nil {
			return ErrLaunchIdentityChanged
		}
		info, err := current.Stat()
		_ = current.Close()
		if err != nil {
			return err
		}
		if !sameImage(image.info, image.change, info) {
			return ErrLaunchIdentityChanged
		}
	}
	return nil
}

func (p *ProcessPins) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	var result error
	if p.cwd != nil {
		result = p.cwd.Close()
	}
	for _, image := range p.images {
		result = errors.Join(result, image.file.Close())
	}
	p.images, p.words, p.paths, p.cwd, p.cwdInfo = nil, nil, nil, nil, nil
	return result
}

func (p *ProcessPins) expectedArguments(original []string) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, ErrLaunchIdentityChanged
	}
	result := cloneVector(original)
	for i, words := range p.words {
		path := p.images[i].path
		if i == 0 && strings.ContainsRune(original[0], '/') {
			path = original[0]
		}
		next := make([]string, 0, len(words)+len(result))
		next = append(next, words...)
		next = append(next, path)
		next = append(next, result[1:]...)
		result = next
	}
	return result, nil
}
