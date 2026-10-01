package bundled

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/filelock"
	"github.com/lycaon/lycaon/internal/fseffect"
)

// ReleaseFetchOptions selects transport and cache policy for an exact release pin.
type ReleaseFetchOptions struct {
	CacheRoot string
	Archive   string
	Offline   bool
	Client    *http.Client
}

// ResolvedRelease contains an admitted artifact and its immutable cache generation.
type ResolvedRelease struct {
	Manifest  *Manifest
	Directory string
}

// FetchReleaseArtifact admits downloaded or imported bytes into a verified cache generation.
func FetchReleaseArtifact(ctx context.Context, source *Manifest, goos, goarch string, options ReleaseFetchOptions) (*ResolvedRelease, error) {
	pin, err := source.ReleaseForPlatform(goos, goarch)
	if err != nil {
		return nil, err
	}
	root, err := releaseCacheRoot(options.CacheRoot)
	if err != nil {
		return nil, err
	}
	lock, err := lockReleaseCache(ctx, root, pin.SHA256)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Close() }()
	archive, err := ensureReleaseArchive(ctx, root, pin, options)
	if err != nil {
		return nil, err
	}
	defer func() { _ = archive.file.Close() }()
	members, err := archive.scan(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("validate release archive: %w", err)
	}
	directory, err := cachedReleaseDirectory(root, pin.SHA256)
	if err != nil {
		return nil, err
	}
	if directory != "" {
		if resolved, err := admitReleaseDirectory(source, directory, goos, goarch, members); err == nil {
			return resolved, nil
		}
	}
	return publishReleaseGeneration(ctx, source, root, goos, goarch, archive)
}

func releaseCacheRoot(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("release cache root is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

func lockReleaseCache(ctx context.Context, root, digest string) (*os.File, error) {
	path := filepath.Join(root, digest+".lock")
	if _, err := candidateRegularFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := filelock.Open(path)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return nil, err
		}
		locked, err := filelock.TryExclusive(file)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		if locked {
			return file, nil
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func ensureReleaseArchive(ctx context.Context, root string, pin *ReleaseArtifact, options ReleaseFetchOptions) (*releaseArchive, error) {
	path := filepath.Join(root, pin.SHA256+".tar.gz")
	if options.Archive == "" {
		archive, err := openPinnedArchive(ctx, path, pin)
		if err == nil {
			return archive, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if err := storeReleaseArchive(ctx, root, pin, options); err != nil {
		return nil, err
	}
	return openPinnedArchive(ctx, path, pin)
}

func cachedReleaseDirectory(root, digest string) (string, error) {
	path := filepath.Join(root, digest+".current")
	file, err := openReleaseFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if info.Size() > 256 {
		return "", fmt.Errorf("invalid release cache generation pointer")
	}
	raw := make([]byte, info.Size())
	if _, err := file.ReadAt(raw, 0); err != nil {
		return "", err
	}
	name := string(raw)
	if !strings.HasPrefix(name, "."+digest+"-") || filepath.Base(name) != name || strings.ContainsAny(name, "/\\\x00\r\n") {
		return "", fmt.Errorf("invalid release cache generation pointer")
	}
	directory := filepath.Join(root, name)
	entry, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !entry.IsDir() || entry.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("release cache generation is not a directory")
	}
	return directory, nil
}

func admitReleaseDirectory(source *Manifest, directory, goos, goarch string, members map[string]PayloadIdentity) (*ResolvedRelease, error) {
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("release directory must not be a symlink")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	expected := releaseMemberNames()
	if len(entries) != len(expected) {
		return nil, fmt.Errorf("release cache generation has unexpected payload count")
	}
	for _, entry := range entries {
		if !expected[entry.Name()] || !entry.Type().IsRegular() {
			return nil, fmt.Errorf("release cache generation has unexpected member")
		}
	}
	selected, err := SelectBuildArtifact(source, directory, goos, goarch)
	if err != nil {
		return nil, err
	}
	if err := verifyReleaseMembers(selected, members); err != nil {
		return nil, err
	}
	selected.releaseAdmitted = true
	return &ResolvedRelease{Manifest: selected, Directory: directory}, nil
}

func publishReleaseGeneration(ctx context.Context, source *Manifest, root, goos, goarch string, archive *releaseArchive) (*ResolvedRelease, error) {
	directory, err := os.MkdirTemp(root, "."+archive.pin.SHA256+"-")
	if err != nil {
		return nil, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(directory)
		}
	}()
	members, err := archive.scan(ctx, directory)
	if err != nil {
		return nil, err
	}
	resolved, err := admitReleaseDirectory(source, directory, goos, goarch, members)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.Location{Root: root, Rel: archive.pin.SHA256 + ".current"}, Source: strings.NewReader(filepath.Base(directory)), Mode: 0o600})
	if err != nil {
		return nil, err
	}
	published = true
	return resolved, nil
}
