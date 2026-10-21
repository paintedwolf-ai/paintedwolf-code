//go:build linux

package launchguard

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
)

// PolicyPath is selected by the administrator, never by the desktop caller.
const PolicyPath = "/etc/paintedwolf/launch.json"

// The installed layout. These are variables only so a test build can place a
// root-owned fixture elsewhere at link time (-ldflags -X); no runtime input,
// environment or argument selects them.
var (
	policyPath    = PolicyPath
	lockDirectory = GenerationLockDirectory
)

// GenerationPolicy binds a physical immutable generation to its logical paths.
type GenerationPolicy struct {
	Version            int    `json:"version"`
	GenerationRoot     string `json:"generation_root"`
	ManifestSHA256     string `json:"manifest_sha256"`
	LogicalInstallRoot string `json:"logical_install_root"`
	// SystemExecutables are root-controlled files or directories outside the
	// generation the protected namespace may execute, such as WebKit's
	// helper programs; nothing else outside the generation can execute there.
	SystemExecutables []string `json:"system_executables,omitempty"`
}

// Generation retains the directory and executable identities selected once at
// launch. Package lifecycle owns the generation's shared lifetime lock.
type Generation struct {
	Lifetime     *os.File
	Directory    *os.File
	Policy       GenerationPolicy
	Root         *os.Root
	Manifest     *pkgmanifest.Manifest
	Desktop      *os.File
	DesktopImage pkgmanifest.Executable
	Entry        *os.File
}

func (g *Generation) Close() {
	if g.Desktop != nil {
		_ = g.Desktop.Close()
	}
	if g.Entry != nil {
		_ = g.Entry.Close()
	}
	if g.Root != nil {
		_ = g.Root.Close()
	}
	if g.Directory != nil {
		_ = g.Directory.Close()
	}
	if g.Lifetime != nil {
		_ = g.Lifetime.Close()
	}
}

// CaptureGeneration accepts only the installed policy. No caller-controlled
// path or role can turn this bootstrap into an arbitrary privileged launcher.
func CaptureGeneration() (*Generation, error) {
	policy, err := ReadPolicy()
	if err != nil {
		return nil, err
	}
	lifetime, err := retainGeneration(policy.ManifestSHA256)
	if err != nil {
		return nil, err
	}
	retained := false
	defer func() {
		if !retained {
			_ = lifetime.Close()
		}
	}()
	directory, err := openRootControlled(policy.GenerationRoot, true)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(fmt.Sprintf("/proc/self/fd/%d", directory.Fd()))
	if err != nil {
		_ = directory.Close()
		return nil, err
	}
	generation := &Generation{Policy: policy, Root: root, Directory: directory, Lifetime: lifetime}
	retained = true
	success := false
	defer func() {
		if !success {
			generation.Close()
		}
	}()
	manifest, err := root.Open("manifest.json")
	if err != nil {
		return nil, err
	}
	defer func() { _ = manifest.Close() }()
	if err := rootRegular(manifest); err != nil {
		return nil, err
	}
	content, err := io.ReadAll(io.LimitReader(manifest, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(content)
	if hex.EncodeToString(digest[:]) != policy.ManifestSHA256 {
		return nil, errors.New("generation manifest differs from installed policy")
	}
	generation.Manifest, err = pkgmanifest.Read(bytes.NewReader(content))
	if err != nil {
		return nil, err
	}
	if generation.Manifest.InstallRoot != policy.LogicalInstallRoot {
		return nil, errors.New("generation logical root differs from installed policy")
	}
	generation.Desktop, generation.DesktopImage, err = generation.openRole("bin/painted-wolf-code")
	if err != nil {
		return nil, err
	}
	generation.Entry, _, err = generation.openRole("bin/pw-launch-entry")
	if err != nil {
		return nil, err
	}
	success = true
	return generation, nil
}

func decodePolicy(raw []byte) (GenerationPolicy, error) {
	var policy GenerationPolicy
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return policy, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return policy, errors.New("launch policy has trailing data")
	}
	digest, err := hex.DecodeString(policy.ManifestSHA256)
	if policy.Version != 1 || err != nil || len(digest) != sha256.Size || strings.ToLower(policy.ManifestSHA256) != policy.ManifestSHA256 {
		return policy, errors.New("invalid launch policy identity")
	}
	for _, path := range policy.SystemExecutables {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return policy, errors.New("invalid launch policy system executable")
		}
	}
	for _, path := range []string{policy.GenerationRoot, policy.LogicalInstallRoot} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return policy, errors.New("invalid launch policy root")
		}
	}
	return policy, nil
}

func (g *Generation) openRole(relative string) (*os.File, pkgmanifest.Executable, error) {
	logical := filepath.Join(g.Policy.LogicalInstallRoot, relative)
	image, ok := g.Manifest.Lookup(logical)
	if !ok {
		return nil, image, errors.New("generation omits a required bootstrap role")
	}
	// Every component is root-controlled and symlink-free. The retained root
	// prevents a later policy replacement from changing this launch's selection.
	parent := "."
	for _, component := range strings.Split(relative, "/") {
		parent = filepath.Join(parent, component)
		info, err := g.Root.Lstat(parent)
		if err != nil {
			return nil, image, err
		}
		if err := trustedObject(info); err != nil {
			return nil, image, err
		}
	}
	file, err := g.Root.Open(relative)
	if err != nil {
		return nil, image, err
	}
	if err := rootRegular(file); err != nil {
		_ = file.Close()
		return nil, image, err
	}
	if err := pkgmanifest.VerifyOpened(file, image); err != nil {
		_ = file.Close()
		return nil, image, err
	}
	image.Path = filepath.Join(g.Policy.GenerationRoot, relative)
	return file, image, nil
}

func openRootControlled(path string, directory bool) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("trusted path must be absolute and clean")
	}
	root, err := os.OpenRoot("/")
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	current := "."
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		current = filepath.Join(current, component)
		info, err := root.Lstat(current)
		if err != nil {
			return nil, err
		}
		if err := trustedObject(info); err != nil {
			return nil, fmt.Errorf("/%s: %w", current, err)
		}
	}
	file, err := root.Open(current)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || info.IsDir() != directory || (!directory && !info.Mode().IsRegular()) {
		_ = file.Close()
		return nil, errors.New("trusted path has an unexpected object type")
	}
	if err := trustedObject(info); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

// ensureControlledDirectory opens path below base one component at a time,
// creating each missing directory 0755 and requiring every component, new or
// existing, to satisfy trusted. Creation is relative to the held parent, so a
// component replaced concurrently is refused rather than followed.
func ensureControlledDirectory(base *os.File, path string, trusted func(os.FileInfo) error) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, errors.New("trusted directory must be absolute, clean and below the root")
	}
	parent, err := unix.Dup(int(base.Fd()))
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(parent), "/")
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if err := unix.Mkdirat(int(current.Fd()), component, 0o755); err == nil {
			// The creator's umask must not decide the directory's mode.
			if err := unix.Fchmodat(int(current.Fd()), component, 0o755, unix.AT_SYMLINK_NOFOLLOW); err != nil && !errors.Is(err, unix.EOPNOTSUPP) {
				_ = current.Close()
				return nil, err
			}
		} else if !errors.Is(err, unix.EEXIST) {
			_ = current.Close()
			return nil, err
		}
		fd, err := unix.Openat(int(current.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = current.Close()
		if err != nil {
			return nil, err
		}
		current = os.NewFile(uintptr(fd), component)
		info, err := current.Stat()
		if err == nil {
			err = trusted(info)
		}
		if err != nil {
			_ = current.Close()
			return nil, err
		}
	}
	return current, nil
}

func trustedObject(info os.FileInfo) error {
	facts, ok := info.Sys().(*syscall.Stat_t)
	if !ok || facts.Uid != 0 || info.Mode()&os.ModeSymlink != 0 || (info.Mode().Perm()&0022 != 0 && !(info.IsDir() && info.Mode()&os.ModeSticky != 0)) {
		return errors.New("launch path is not exclusively root-controlled")
	}
	return nil
}

// GenerationLockDirectory holds one persistent lifetime lock per generation.
// It lives on the /run tmpfs, so it is absent after every boot until the first
// privileged launch or package transaction creates it.
const GenerationLockDirectory = "/run/paintedwolf/generation-locks"

// The package collector uses the same persistent lock inode and never unlinks
// it during a boot. A selected generation cannot be reclaimed under a launch.
func retainGeneration(digest string) (*os.File, error) {
	root, err := os.Open("/")
	if err != nil {
		return nil, err
	}
	directory, err := ensureControlledDirectory(root, lockDirectory, trustedObject)
	_ = root.Close()
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	fd, err := unix.Openat(int(directory.Fd()), digest+".lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "generation-lifetime")
	if err := rootRegular(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := unix.Flock(fd, unix.LOCK_SH|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("generation is being reclaimed: %w", err)
	}
	return file, nil
}

// ReadPolicy reads the administrator-selected launch generation without
// opening it. The collector rechecks this selection while holding its lock.
func ReadPolicy() (GenerationPolicy, error) {
	policyFile, err := openRootControlled(policyPath, false)
	if err != nil {
		return GenerationPolicy{}, err
	}
	defer func() { _ = policyFile.Close() }()
	raw, err := io.ReadAll(io.LimitReader(policyFile, 16385))
	if err != nil {
		return GenerationPolicy{}, err
	}
	if len(raw) > 16384 {
		return GenerationPolicy{}, errors.New("launch policy exceeds its bound")
	}
	return decodePolicy(raw)
}
