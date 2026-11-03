package referenceuserland

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
)

// ID names a reference userland.
type ID string

const (
	GNU     ID = "gnu"
	BSD     ID = "bsd"
	BusyBox ID = "busybox"
)

// IDs lists every reference userland.
var IDs = []ID{GNU, BSD, BusyBox}

// Architectures are the image platforms each userland records a tree digest for.
var Architectures = []string{"amd64", "arm64"}

// ManifestPath is the manifest's location relative to the Go module root.
const ManifestPath = "test/referenceuserland/userlands.json"

//go:embed userlands.json *.Containerfile
var definitions embed.FS

// Source is an upstream source archive pinned by its hash.
type Source struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
}

// Userland is one reference userland's pinned image definition.
type Userland struct {
	ID            ID                `json:"-"`
	Containerfile string            `json:"containerfile"`
	Shell         string            `json:"shell"`
	Base          string            `json:"base"`
	Snapshot      string            `json:"snapshot,omitempty"`
	Packages      map[string]string `json:"packages,omitempty"`
	BuildPackages map[string]string `json:"build_packages,omitempty"`
	// ArchitectureVersions overrides, per architecture, the versions of packages the
	// archive rebuilt for that architecture alone.
	ArchitectureVersions map[string]map[string]string `json:"architecture_versions,omitempty"`
	Replaced             []string                     `json:"replaced,omitempty"`
	Sources              map[string]Source            `json:"sources,omitempty"`
	// Trees holds, per architecture, the digest of the image's file tree.
	Trees map[string]string `json:"trees"`
}

// Manifest is the set of pinned reference userlands.
type Manifest struct {
	Userlands map[ID]*Userland `json:"userlands"`
}

var (
	digestPattern   = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
	snapshotPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z$`)
	hashPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	treePattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	sourcePattern   = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
)

// LoadManifest reads the manifest built into this package.
func LoadManifest() (*Manifest, error) {
	data, err := definitions.ReadFile("userlands.json")
	if err != nil {
		return nil, err
	}
	return ParseManifest(data)
}

// ParseManifest decodes and validates a manifest.
func ParseManifest(data []byte) (*Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var m Manifest
	if err := decoder.Decode(&m); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	if got := slices.Sorted(maps.Keys(m.Userlands)); !slices.Equal(got, slices.Sorted(slices.Values(IDs))) {
		return nil, fmt.Errorf("manifest declares userlands %v, want %v", got, IDs)
	}
	for id, u := range m.Userlands {
		u.ID = id
		if err := u.validate(); err != nil {
			return nil, fmt.Errorf("userland %s: %w", id, err)
		}
	}
	return &m, nil
}

// Encode renders the manifest in its canonical form.
func (m *Manifest) Encode() ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Userland returns the definition of id.
func (m *Manifest) Userland(id ID) (*Userland, error) {
	u, ok := m.Userlands[id]
	if !ok {
		return nil, fmt.Errorf("unknown userland %q; known: %v", id, IDs)
	}
	return u, nil
}

func (u *Userland) validate() error {
	if _, err := definitions.ReadFile(u.Containerfile); err != nil || path.Base(u.Containerfile) != u.Containerfile {
		return fmt.Errorf("containerfile %q is not a definition in this package", u.Containerfile)
	}
	if !path.IsAbs(u.Shell) {
		return fmt.Errorf("shell %q is not an absolute path", u.Shell)
	}
	if !digestPattern.MatchString(u.Base) {
		return fmt.Errorf("base %q is not pinned by digest", u.Base)
	}
	if (len(u.Packages)+len(u.BuildPackages) > 0) != (u.Snapshot != "") {
		return fmt.Errorf("packages and an archive snapshot come together")
	}
	if u.Snapshot != "" && !snapshotPattern.MatchString(u.Snapshot) {
		return fmt.Errorf("snapshot %q is not an archive timestamp", u.Snapshot)
	}
	all := packageVersions(u.Packages, u.BuildPackages)
	for name, version := range all {
		if name == "" || version == "" || strings.ContainsAny(name+version, " =") {
			return fmt.Errorf("package %q=%q is not an exact pin", name, version)
		}
	}
	for arch, versions := range u.ArchitectureVersions {
		if !slices.Contains(Architectures, arch) {
			return fmt.Errorf("versions for unknown architecture %q", arch)
		}
		for name, version := range versions {
			if _, ok := all[name]; !ok || version == "" || version == all[name] || strings.ContainsAny(version, " =") {
				return fmt.Errorf("%s version %q of %q does not override a pinned package", arch, version, name)
			}
		}
	}
	for _, name := range u.Replaced {
		if _, kept := u.Packages[name]; name == "" || kept {
			return fmt.Errorf("replaced package %q is also installed", name)
		}
	}
	for name, s := range u.Sources {
		if !sourcePattern.MatchString(name) || s.Version == "" || !strings.Contains(s.URL, s.Version) ||
			!strings.HasPrefix(s.URL, "https://") || !hashPattern.MatchString(s.SHA256) {
			return fmt.Errorf("source %q is not pinned by version, https URL and sha256", name)
		}
	}
	for arch, tree := range u.Trees {
		if !slices.Contains(Architectures, arch) || !treePattern.MatchString(tree) {
			return fmt.Errorf("tree %q=%q is not a recorded architecture digest", arch, tree)
		}
	}
	return nil
}

func packageVersions(sets ...map[string]string) map[string]string {
	all := map[string]string{}
	for _, set := range sets {
		maps.Copy(all, set)
	}
	return all
}

// Definition returns the userland's Containerfile.
func (u *Userland) Definition() ([]byte, error) {
	return definitions.ReadFile(u.Containerfile)
}

// BuildArgs are the build arguments that carry the userland's pins for arch into its Containerfile.
func (u *Userland) BuildArgs(arch string) []string {
	args := []string{"BASE=" + u.Base}
	if u.Snapshot != "" {
		args = append(args, "SNAPSHOT="+u.Snapshot)
	}
	if len(u.Packages) > 0 {
		args = append(args, "PACKAGES="+pins(u.Packages, u.ArchitectureVersions[arch]))
	}
	if len(u.BuildPackages) > 0 {
		args = append(args, "BUILD_PACKAGES="+pins(u.BuildPackages, u.ArchitectureVersions[arch]))
	}
	if len(u.Replaced) > 0 {
		args = append(args, "REPLACED="+strings.Join(u.Replaced, " "))
	}
	for _, name := range slices.Sorted(maps.Keys(u.Sources)) {
		s, key := u.Sources[name], strings.ToUpper(name)
		args = append(args, key+"_URL="+s.URL, key+"_SHA256="+s.SHA256)
	}
	return args
}

func pins(packages, overrides map[string]string) string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(packages)) {
		version := packages[name]
		if override, ok := overrides[name]; ok {
			version = override
		}
		out = append(out, name+"="+version)
	}
	return strings.Join(out, " ")
}

// Tag names the verified image of the userland for arch. It changes whenever the
// definition, a pin, or the recorded tree changes, so a stale image is never reused.
func (u *Userland) Tag(arch string) (string, error) {
	tree, ok := u.Trees[arch]
	if !ok {
		return "", fmt.Errorf("userland %s records no tree for %s; run ./task userlands:record", u.ID, arch)
	}
	definition, err := u.Definition()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write(definition)
	for _, arg := range u.BuildArgs(arch) {
		h.Write([]byte{0})
		h.Write([]byte(arg))
	}
	h.Write([]byte{0})
	h.Write([]byte(tree))
	return fmt.Sprintf("%s:%s-%s-%s", imageRepository, u.ID, arch, hex.EncodeToString(h.Sum(nil))[:12]), nil
}

const imageRepository = "paintedwolf-reference-userland"
