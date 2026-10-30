//go:build linux

package packagegeneration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
)

// LaunchCatalog is the sealed payload file naming, per package family and CPU,
// the system programs the protected namespace may execute (WebKit's helper
// processes) and the library roots whose native code it may map. Its source is
// packaging/linux/protected-launch.json.
const LaunchCatalog = "protected-launch.json"

const logicalInstallRoot = "/opt/painted-wolf-code"

var generationName = regexp.MustCompile(`^(\.install-)?([0-9a-f]{64})$`)

type launchCatalog struct {
	FormatVersion int `json:"format_version"`
	Architectures map[string]struct {
		Multiarch string `json:"multiarch"`
	} `json:"architectures"`
	Families map[string]struct {
		SystemExecutables []string `json:"system_executables"`
		LibraryRoots      []string `json:"library_roots"`
	} `json:"families"`
}

// SystemAccess is what the launch policy grants outside the generation, resolved
// for the installing family and CPU from what the system has.
type SystemAccess struct {
	Executables  []string
	LibraryRoots []string
}

// Install materializes the package's staged payload as an immutable generation,
// selects it for new launches through commit, and reclaims every generation that
// is neither selected nor in use. family is the installing package family.
func Install(family string, commit func(Generation, SystemAccess) error, selection func() (string, error)) (Generation, error) {
	if os.Geteuid() != 0 {
		return Generation{}, errors.New("native generation: installation requires root")
	}
	staging, err := os.OpenRoot(logicalInstallRoot)
	if err != nil {
		return Generation{}, err
	}
	defer func() { _ = staging.Close() }()
	return native.install(staging, family, runtime.GOARCH, commit, selection)
}

// Remove withdraws the selection through removePolicy, then reclaims every
// generation no running launch holds; held generations stay until the next
// package transaction.
func Remove(removePolicy func() error, selection func() (string, error)) error {
	if os.Geteuid() != 0 {
		return errors.New("native generation: removal requires root")
	}
	return native.remove(removePolicy, selection)
}

func (l layout) install(staging *os.Root, family, arch string, commit func(Generation, SystemAccess) error, selection func() (string, error)) (Generation, error) {
	if commit == nil || selection == nil {
		return Generation{}, errors.New("native generation: missing protected policy owner")
	}
	info, err := staging.Lstat("manifest.json")
	if err != nil {
		return Generation{}, err
	}
	if info.Size() <= 0 || info.Size() > 4<<20 {
		return Generation{}, errors.New("native generation: staged manifest is out of bounds")
	}
	raw, err := readSeal(staging, int(info.Size()), l.owner)
	if err != nil {
		return Generation{}, err
	}
	generation, err := l.materialize(staging, raw)
	if err != nil {
		return Generation{}, err
	}
	root, err := os.OpenRoot(generation.Root)
	if err != nil {
		return Generation{}, err
	}
	defer func() { _ = root.Close() }()
	access, err := l.systemAccess(root, family, arch)
	if err != nil {
		return Generation{}, err
	}
	if err := l.selectGeneration(generation.ManifestSHA256, func(selected Generation) error {
		return commit(selected, access)
	}); err != nil {
		return Generation{}, err
	}
	return generation, l.reclaimUnselected(selection)
}

func (l layout) remove(removePolicy func() error, selection func() (string, error)) error {
	if removePolicy == nil || selection == nil {
		return errors.New("native generation: missing protected policy owner")
	}
	if err := removePolicy(); err != nil {
		return err
	}
	return l.reclaimUnselected(selection)
}

// reclaimUnselected visits every generation and interrupted installation in the
// store; reclaim itself refuses the selected generation and any held one.
func (l layout) reclaimUnselected(selection func() (string, error)) error {
	entries, err := os.ReadDir(l.store)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		match := generationName.FindStringSubmatch(entry.Name())
		if match == nil {
			failures = append(failures, fmt.Errorf("native generation: foreign store entry %q", entry.Name()))
			continue
		}
		if _, err := l.reclaim(match[2], selection); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// systemAccess resolves the sealed catalog for this family and CPU to what the
// system has now. A program or library root the distribution does not ship is
// left out; one that is present but not controlled by root refuses the
// installation, as does a family with no helper program or library root.
func (l layout) systemAccess(generation *os.Root, family, arch string) (SystemAccess, error) {
	file, err := generation.Open(LaunchCatalog)
	if err != nil {
		return SystemAccess{}, fmt.Errorf("native generation: sealed launch catalog: %w", err)
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return SystemAccess{}, err
	}
	if len(raw) > 1<<20 {
		return SystemAccess{}, errors.New("native generation: launch catalog exceeds its bound")
	}
	var catalog launchCatalog
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return SystemAccess{}, fmt.Errorf("native generation: decode launch catalog: %w", err)
	}
	if catalog.FormatVersion != 1 {
		return SystemAccess{}, fmt.Errorf("native generation: unsupported launch catalog format %d", catalog.FormatVersion)
	}
	selected, ok := catalog.Families[family]
	if !ok {
		return SystemAccess{}, fmt.Errorf("native generation: launch catalog has no %q family", family)
	}
	cpu, ok := catalog.Architectures[arch]
	if !ok || cpu.Multiarch == "" {
		return SystemAccess{}, fmt.Errorf("native generation: launch catalog has no %q architecture", arch)
	}
	executables, err := l.present(selected.SystemExecutables, cpu.Multiarch, false)
	if err != nil {
		return SystemAccess{}, err
	}
	if len(executables) == 0 {
		return SystemAccess{}, fmt.Errorf("native generation: no WebKitGTK helper program for %s is installed", family)
	}
	roots, err := l.present(selected.LibraryRoots, cpu.Multiarch, true)
	if err != nil {
		return SystemAccess{}, err
	}
	if len(roots) == 0 {
		return SystemAccess{}, fmt.Errorf("native generation: no library root for %s is present", family)
	}
	return SystemAccess{Executables: executables, LibraryRoots: roots}, nil
}

// present resolves templates and keeps the root-controlled programs (or, with
// directories, physical directories) that exist.
func (l layout) present(templates []string, multiarch string, directories bool) ([]string, error) {
	var kept []string
	for _, template := range templates {
		path := strings.ReplaceAll(template, "{multiarch}", multiarch)
		if strings.ContainsAny(path, "{}") || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return nil, fmt.Errorf("native generation: invalid launch catalog path %q", template)
		}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		kind := info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
		if directories {
			kind = info.IsDir()
		}
		if !kind || !ok || stat.Uid != 0 && stat.Uid != l.owner || info.Mode().Perm()&0o022 != 0 {
			return nil, fmt.Errorf("native generation: %s is not root-controlled system code", path)
		}
		kept = append(kept, path)
	}
	return kept, nil
}
