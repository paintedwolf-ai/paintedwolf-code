//go:build darwin

package configlayout

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"golang.org/x/sys/unix"
)

// PrivateGuestPayload holds immutable boot files until the VM helper inherits them.
type PrivateGuestPayload struct {
	Helper string
	CDHash string
	Boot   []*os.File
}

func (p *PrivateGuestPayload) Close() {
	for _, file := range p.Boot {
		_ = file.Close()
	}
	p.Boot = nil
}

// OpenPrivateGuestPayload resolves the runtime from the running installation.
func OpenPrivateGuestPayload() (*PrivateGuestPayload, error) {
	host, err := os.Executable()
	if err != nil {
		return nil, err
	}
	root, err := configdir.Root()
	if err != nil {
		return nil, err
	}
	contents := MacOSAppContents(host)
	if contents != "" {
		return privateGuestPayload(filepath.Join(contents, "Resources", "service-guest"), root, filepath.Dir(contents))
	}
	if configdir.IsDevelopmentBuild() {
		return privateGuestPayload(filepath.Join(filepath.Dir(host), "service-guest"), root, "")
	}
	return nil, errors.New("private service runtime missing from application bundle")
}

func privateGuestPayload(bundle, root, app string) (_ *PrivateGuestPayload, err error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("private service storage requires an absolute path")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(root, "service-boot-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	p := &PrivateGuestPayload{Helper: filepath.Join(bundle, "pw-service-vm")}
	defer func() {
		if err != nil {
			p.Close()
		}
	}()
	for _, spec := range []struct {
		name  string
		limit int64
	}{{"manifest.json", 64 << 10}, {"kernel", 1 << 30}, {"initramfs.gz", 1 << 30}} {
		dest := filepath.Join(dir, spec.name)
		if err := snapshotGuestFile(filepath.Join(bundle, spec.name), dest, spec.limit); err != nil {
			return nil, fmt.Errorf("stage private service %s: %w", spec.name, err)
		}
		file, err := os.Open(dest)
		if err != nil {
			return nil, err
		}
		p.Boot = append(p.Boot, file)
		if err := os.Remove(dest); err != nil {
			return nil, err
		}
		if err := verifyUnlinkedBootFile(file); err != nil {
			return nil, err
		}
	}
	raw, err := readGuestResource(filepath.Join(bundle, "pw-service-vm.cdhash"), 41)
	if err != nil {
		return nil, err
	}
	p.CDHash = strings.TrimSuffix(string(raw), "\n")
	digest, err := hex.DecodeString(p.CDHash)
	if err != nil || len(digest) != 20 {
		return nil, errors.New("invalid private service helper code identity")
	}
	if app != "" {
		manifest, err := io.ReadAll(io.LimitReader(p.Boot[0], 64<<10))
		if err != nil {
			return nil, err
		}
		if err := verifyPrivateGuestSeal(app, p.Helper, manifest, raw); err != nil {
			return nil, err
		}
		if _, err := p.Boot[0].Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func openGuestResource(source string) (*os.File, error) {
	fd, err := unix.Open(source, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), source), nil
}

func readGuestResource(source string, limit int64) ([]byte, error) {
	file, err := openGuestResource(source)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > limit {
		return nil, errors.New("invalid private service resource")
	}
	bytes, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(bytes)) != info.Size() {
		return nil, errors.New("private service resource changed during capture")
	}
	return bytes, nil
}

func snapshotGuestFile(source, dest string, limit int64) error {
	file, err := openGuestResource(source)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > limit {
		return errors.New("invalid boot resource")
	}
	err = unix.Fclonefileat(int(file.Fd()), unix.AT_FDCWD, dest, unix.CLONE_NOOWNERCOPY)
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.EXDEV) && !errors.Is(err, unix.ENOTSUP) {
		return err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0400)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(out, io.LimitReader(file, limit+1))
	if err := errors.Join(copyErr, out.Close()); err != nil {
		return err
	}
	if n != info.Size() {
		return errors.New("boot resource changed during capture")
	}
	return nil
}
