//go:build linux

package installbinding

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/pkgmanifest"
)

const maxManifestBytes = 4 << 20

// Binding is the captured identity of the running installation.
type Binding struct {
	root         string
	manifestPath string
	manifest     []byte
	entries      map[string]pkgmanifest.Executable

	mu           sync.Mutex
	manifestFile fileID
	verified     map[string]fileID
}

type fileID struct {
	dev, ino         uint64
	size             int64
	mtimeNs, ctimeNs int64
}

var current atomic.Pointer[Binding]

// Install makes b the process-wide binding. A nil binding checks nothing.
func Install(b *Binding) { current.Store(b) }

// Verify checks an executable this process is about to launch.
func Verify(path string) error {
	if b := current.Load(); b != nil {
		return b.Verify(path)
	}
	return nil
}

// CaptureRunning binds an engine started from the Linux package installation.
// Development, Nix and other unpackaged engines have no package to bind and
// return nil.
func CaptureRunning() (*Binding, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, err
	}
	if filepath.Dir(resolved) != filepath.Join(configlayout.LinuxInstallRoot, "bin") {
		return nil, nil
	}
	// The running image, even if its pathname has already been replaced.
	image, err := os.Open("/proc/self/exe")
	if err != nil {
		return nil, err
	}
	defer func() { _ = image.Close() }()
	return Capture(configlayout.LinuxInstallRoot, pkgmanifest.DefaultPath, resolved, image)
}

// Capture binds the installation at root, described by manifestPath, after
// confirming the manifest lists executable with the bytes of image, the running
// program opened so that a replaced pathname cannot stand in for it.
func Capture(root, manifestPath, executable string, image *os.File) (*Binding, error) {
	file, err := os.Open(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read installed manifest: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxManifestBytes {
		return nil, errors.New("installed manifest exceeds its bound")
	}
	manifest, err := pkgmanifest.Read(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	b := &Binding{root: filepath.Clean(root), manifestPath: manifestPath, manifest: raw,
		entries: map[string]pkgmanifest.Executable{}, manifestFile: identity(info), verified: map[string]fileID{}}
	for _, entry := range manifest.Executables {
		b.entries[entry.Path] = entry
	}
	entry, ok := b.entries[executable]
	if !ok {
		return nil, fmt.Errorf("installed manifest does not list the running engine %s", executable)
	}
	if err := pkgmanifest.VerifyOpened(image, entry); err != nil {
		return nil, fmt.Errorf("%w (%v)", ErrReplaced, err)
	}
	return b, nil
}

// Verify accepts paths outside the installation, and installed executables whose
// bytes still match the captured manifest.
func (b *Binding) Verify(path string) error {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || clean != b.root && !strings.HasPrefix(clean, b.root+string(os.PathSeparator)) {
		return nil
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return fmt.Errorf("%w (%v)", ErrReplaced, err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.unchangedManifest(); err != nil {
		return err
	}
	entry, ok := b.entries[resolved]
	if !ok {
		return fmt.Errorf("%w (%s is not part of the running installation)", ErrReplaced, resolved)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("%w (%v)", ErrReplaced, err)
	}
	if known, ok := b.verified[resolved]; ok && known == identity(info) {
		return nil
	}
	file, err := os.Open(resolved)
	if err != nil {
		return fmt.Errorf("%w (%v)", ErrReplaced, err)
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	if err := pkgmanifest.VerifyOpened(file, entry); err != nil {
		return fmt.Errorf("%w (%v)", ErrReplaced, err)
	}
	b.verified[resolved] = identity(opened)
	return nil
}

func (b *Binding) unchangedManifest() error {
	info, err := os.Stat(b.manifestPath)
	if err != nil {
		return fmt.Errorf("%w (%v)", ErrReplaced, err)
	}
	if identity(info) == b.manifestFile {
		return nil
	}
	raw, err := os.ReadFile(b.manifestPath)
	if err != nil {
		return fmt.Errorf("%w (%v)", ErrReplaced, err)
	}
	if sha256.Sum256(raw) != sha256.Sum256(b.manifest) {
		return ErrReplaced
	}
	b.manifestFile = identity(info)
	return nil
}

func identity(info os.FileInfo) fileID {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{size: info.Size(), mtimeNs: info.ModTime().UnixNano()}
	}
	return fileID{dev: stat.Dev, ino: stat.Ino, size: stat.Size, mtimeNs: stat.Mtim.Nano(), ctimeNs: stat.Ctim.Nano()}
}
