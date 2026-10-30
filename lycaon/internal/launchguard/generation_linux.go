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
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
)

// PolicyPath is selected by the administrator, never by the desktop caller.
const PolicyPath = "/etc/paintedwolf/launch.json"

// The installed layout. These are variables only so a test build can place a
// root-owned fixture elsewhere at link time (-ldflags -X); no runtime input,
// environment or argument selects them. Production builds anchor at "/" and
// require uid 0 on every ancestor and object.
var (
	policyPath           = PolicyPath
	lockDirectory        = GenerationLockDirectory
	trustAnchor          = "/"
	trustedOwner  uint32 = 0
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
	// LibraryRoots are the root-controlled directories outside the generation
	// whose native code the protected namespace may map executable: the
	// system's library directories, or a Nix generation's closure. Every other
	// mount is noexec there.
	LibraryRoots []string `json:"library_roots"`
}

// Generation retains the directory and executable identities selected once at
// launch. Package lifecycle owns the generation's shared lifetime lock.
type Generation struct {
	Lifetime        *os.File
	Directory       *os.File
	Policy          GenerationPolicy
	Root            *os.Root
	Manifest        *pkgmanifest.Manifest
	Desktop         *os.File
	DesktopImage    pkgmanifest.Executable
	Entry           *os.File
	MountTransition *os.File
}

func (g *Generation) Close() {
	if g.Desktop != nil {
		_ = g.Desktop.Close()
	}
	if g.Entry != nil {
		_ = g.Entry.Close()
	}
	if g.MountTransition != nil {
		_ = g.MountTransition.Close()
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

// CaptureGeneration accepts only the installed policy. It acquires the
// generation's shared lifetime lock and then re-reads the policy so the policy
// and retained generation come from the same locked state.
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
	rechecked, err := ReadPolicy()
	if err != nil {
		return nil, err
	}
	if !samePolicy(policy, rechecked) {
		return nil, errors.New("launch policy changed before generation lock was acquired")
	}
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
	manifest, err := openControlledAt(directory, "manifest.json", false)
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
	generation.MountTransition, _, err = generation.openRole("bin/pw-mount-intake-transition")
	if err != nil {
		return nil, err
	}
	success = true
	return generation, nil
}

func samePolicy(a, b GenerationPolicy) bool {
	return a.Version == b.Version &&
		a.GenerationRoot == b.GenerationRoot &&
		a.ManifestSHA256 == b.ManifestSHA256 &&
		a.LogicalInstallRoot == b.LogicalInstallRoot &&
		slices.Equal(a.SystemExecutables, b.SystemExecutables) &&
		slices.Equal(a.LibraryRoots, b.LibraryRoots)
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
	if len(policy.LibraryRoots) == 0 {
		return policy, errors.New("launch policy names no library roots")
	}
	for _, path := range policy.LibraryRoots {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return policy, errors.New("invalid launch policy library root")
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
	file, err := openControlledAt(g.Directory, relative, false)
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

// GenerationLockDirectory holds one persistent lifetime lock per generation.
// It lives on the /run tmpfs, so it is absent after every boot until the first
// privileged launch or package transaction creates it.
const GenerationLockDirectory = "/run/paintedwolf/generation-locks"

// The package collector uses the same persistent lock inode and never unlinks
// it during a boot. A selected generation cannot be reclaimed under a launch.
func retainGeneration(digest string) (*os.File, error) {
	anchor, rel, err := resolveAnchor(lockDirectory)
	if err != nil || rel == "" {
		return nil, errors.New("invalid generation lock directory")
	}
	root, err := openAnchor(anchor)
	if err != nil {
		return nil, err
	}
	directory, err := ensureControlledDirectory(root, "/"+rel, trustedObject)
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
