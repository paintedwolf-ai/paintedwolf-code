package configlayout

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// RetainRunningNix registers only this application's indirect root. The lock
// separates live sessions from roots left by a crashed process.
func RetainRunningNix(ctx context.Context) (func(), error) {
	layout, err := RunningNix()
	if err != nil {
		return nil, err
	}
	if layout == nil {
		return func() {}, nil
	}
	cache, err := os.UserCacheDir()
	if err != nil || !filepath.IsAbs(cache) {
		return nil, errors.New("Nix retention cache directory unavailable")
	}
	return retainNix(ctx, layout, filepath.Join(cache, "paintedwolf", "nix-running"))
}

func retainNix(ctx context.Context, layout *NixLayout, directory string) (func(), error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err := unix.Lstat(directory, &st); err != nil {
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != uint32(os.Getuid()) || st.Mode&0o077 != 0 {
		return nil, errors.New("Nix retention directory must be private and owned by this user")
	}
	registry, err := lockNixRegistry(directory)
	if err != nil {
		return nil, err
	}
	defer func() {
		if registry != nil {
			_ = registry.Close()
		}
	}()
	if err := pruneNixRoots(directory); err != nil {
		return nil, err
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	link := filepath.Join(directory, "session-"+hex.EncodeToString(nonce))
	lockPath := link + ".lock"
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		_ = os.Remove(lockPath)
		return nil, err
	}
	// A second launch must not prune the new file before its session lock exists.
	_ = registry.Close()
	registry = nil
	var once sync.Once
	release := func() {
		once.Do(func() {
			_ = removeNixLink(link)
			_ = os.Remove(lockPath)
			_ = file.Close()
		})
	}
	operation, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(operation, layout.NixStore, "--add-root", link, "--indirect", "--realise", layout.Output) // #nosec G204 -- exact package-owned Nix executable; no PATH lookup
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "LYCAON_") {
			command.Env = append(command.Env, value)
		}
	}
	command.Stdin = nil
	var diagnostic boundedNixOutput
	command.Stdout, command.Stderr = &diagnostic, &diagnostic
	if err := command.Run(); err != nil {
		release()
		return nil, fmt.Errorf("retain running Nix generation: %w: %s", err, diagnostic.String())
	}
	target, err := os.Readlink(link)
	if err != nil || target != layout.Output {
		release()
		return nil, errors.New("Nix did not register the running generation root")
	}
	return release, nil
}

func lockNixRegistry(directory string) (*os.File, error) {
	path := filepath.Join(directory, "registry.lock")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Getuid()) || st.Mode&0o077 != 0 || st.Nlink != 1 {
		_ = file.Close()
		return nil, errors.New("Nix root registry lock is not a private owned file")
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func pruneNixRoots(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "session-") || !strings.HasSuffix(name, ".lock") || len(name) != 45 {
			continue
		}
		if _, err := hex.DecodeString(name[8:40]); err != nil {
			continue
		}
		path := filepath.Join(directory, name)
		fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			continue
		}
		file := os.NewFile(uintptr(fd), path)
		var st unix.Stat_t
		if unix.Fstat(fd, &st) == nil && st.Mode&unix.S_IFMT == unix.S_IFREG && st.Uid == uint32(os.Getuid()) && st.Mode&0o077 == 0 && st.Nlink == 1 && unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) == nil {
			link := strings.TrimSuffix(path, ".lock")
			if removeNixLink(link) == nil {
				_ = os.Remove(path)
			}
		}
		_ = file.Close()
	}
	return nil
}

func removeNixLink(path string) error {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink == 0 {
		return errors.New("Nix root path is not an owned symlink")
	}
	return os.Remove(path)
}

type boundedNixOutput struct{ bytes.Buffer }

func (b *boundedNixOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 4096 - b.Len()
	if remaining > 0 {
		_, _ = b.Buffer.Write(p[:min(n, remaining)])
	}
	return n, nil
}
