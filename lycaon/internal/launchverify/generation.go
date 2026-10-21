package launchverify

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
)

const (
	// DefaultInstallRoot is the logical installation root in packaged manifests.
	DefaultInstallRoot = "/opt/painted-wolf-code"
	// DefaultGenerationsDir holds immutable materialized payload generations.
	DefaultGenerationsDir = "/opt/painted-wolf-code/generations"
	// DefaultPolicyPath is the administrator-selected active generation policy.
	DefaultPolicyPath = "/etc/paintedwolf/launch.json"
	// DefaultLocksDir holds one persistent shared lifetime lock per generation.
	DefaultLocksDir = "/run/paintedwolf/generation-locks"

	maxPolicyBytes             = 16 << 10
	maxGenerationManifestBytes = 4 << 20
)

type generationPolicy struct {
	Version            int      `json:"version"`
	GenerationRoot     string   `json:"generation_root"`
	ManifestSHA256     string   `json:"manifest_sha256"`
	LogicalInstallRoot string   `json:"logical_install_root"`
	SystemExecutables  []string `json:"system_executables,omitempty"`
}

type resolvedGeneration struct {
	sha      string
	manifest *pkgmanifest.Manifest
	roles    map[string]fileFacts
}

type generationLayout struct {
	procRoot       string
	generationsDir string
	policyPath     string
	locksDir       string
	anchor         string
	owner          uint32
}

func (v Verifier) layout() generationLayout {
	return generationLayout{
		procRoot:       cmp.Or(v.ProcRoot, "/proc"),
		generationsDir: cmp.Or(v.GenerationsDir, DefaultGenerationsDir),
		policyPath:     cmp.Or(v.PolicyPath, DefaultPolicyPath),
		locksDir:       cmp.Or(v.LocksDir, DefaultLocksDir),
		anchor:         cmp.Or(v.TrustAnchor, "/"),
		owner:          v.TrustedOwner,
	}
}

// resolve establishes the package generation the peer is running from kernel
// facts: the peer's executable device/inode under generations/<sha>/ and
// either the active launch policy or an active shared generation lifetime lock.
func (g generationLayout) resolve(enginePID, launcherPID int) (resolvedGeneration, error) {
	activePolicy, err := g.readActivePolicy()
	if err != nil {
		return resolvedGeneration{}, refuse(CodeManifestUnreadable, RoleEngine, enginePID, err.Error())
	}
	activeGen, err := g.loadGeneration(activePolicy.ManifestSHA256)
	if err != nil {
		return resolvedGeneration{}, refuse(CodeManifestUnreadable, RoleEngine, enginePID, err.Error())
	}
	if activeGen.manifest.InstallRoot != activePolicy.LogicalInstallRoot {
		return resolvedGeneration{}, refuse(CodeManifestUnreadable, RoleEngine, enginePID, "generation logical root differs from launch policy")
	}
	engineExe, err := runningExecutableFacts(g.procRoot, enginePID, RoleEngine)
	if err != nil {
		return resolvedGeneration{}, err
	}
	if _, err := runningExecutableFacts(g.procRoot, launcherPID, RoleDesktopApp); err != nil {
		return resolvedGeneration{}, err
	}
	if sameInode(engineExe, activeGen.roles[RoleEngine]) {
		return activeGen, nil
	}
	entries, err := os.ReadDir(g.generationsDir)
	if err != nil {
		return resolvedGeneration{}, refuse(CodeManifestUnreadable, RoleEngine, enginePID, err.Error())
	}
	for _, entry := range entries {
		sha := entry.Name()
		if sha == activeGen.sha || !validGenerationDigest(sha) || !g.generationLocked(sha, launcherPID) {
			continue
		}
		gen, err := g.loadGeneration(sha)
		if err != nil {
			continue
		}
		if sameInode(engineExe, gen.roles[RoleEngine]) {
			return gen, nil
		}
	}
	return resolvedGeneration{}, refuse(CodeExecutableMismatch, RoleEngine, enginePID, "executable does not belong to an active or locked package generation")
}

func (g generationLayout) unchanged(chain Chain) error {
	if chain.Generation == "" {
		return nil
	}
	if policy, err := g.readActivePolicy(); err == nil && policy.ManifestSHA256 == chain.Generation {
		if _, err := g.loadGeneration(chain.Generation); err == nil {
			return nil
		}
	}
	if g.generationLocked(chain.Generation, chain.Launcher.PID) {
		if _, err := g.loadGeneration(chain.Generation); err == nil {
			return nil
		}
	}
	return refuse(CodeExecutableMismatch, RoleEngine, chain.Engine.PID, "generation is no longer active or locked")
}

func runningExecutableFacts(procRoot string, pid int, role string) (fileFacts, error) {
	file, err := os.Open(filepath.Join(procRoot, strconv.Itoa(pid), "exe"))
	if err != nil {
		return fileFacts{}, refuse(CodeExecutableUnreadable, role, pid, err.Error())
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return fileFacts{}, refuse(CodeExecutableUnreadable, role, pid, err.Error())
	}
	facts, err := factsOf(info)
	if err != nil {
		return fileFacts{}, refuse(CodeExecutableUnreadable, role, pid, err.Error())
	}
	return facts, nil
}

func sameInode(a, b fileFacts) bool {
	return a.device == b.device && a.inode == b.inode && a.inode != 0
}

func (g generationLayout) readActivePolicy() (generationPolicy, error) {
	dir := filepath.Dir(g.policyPath)
	if err := controlledAncestors(dir, g.anchor, g.owner); err != nil {
		return generationPolicy{}, err
	}
	root, err := openControlledRoot(dir, g.anchor, g.owner)
	if err != nil {
		return generationPolicy{}, err
	}
	defer func() { _ = root.Close() }()
	raw, err := readControlledFile(root, filepath.Base(g.policyPath), maxPolicyBytes, g.owner)
	if err != nil {
		return generationPolicy{}, err
	}
	var policy generationPolicy
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return generationPolicy{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return generationPolicy{}, errors.New("launch policy has trailing data")
	}
	if policy.Version != 1 || !validGenerationDigest(policy.ManifestSHA256) {
		return generationPolicy{}, errors.New("invalid launch policy identity")
	}
	if policy.GenerationRoot != filepath.Join(g.generationsDir, policy.ManifestSHA256) {
		return generationPolicy{}, errors.New("launch policy generation root outside generations directory")
	}
	if !filepath.IsAbs(policy.LogicalInstallRoot) || filepath.Clean(policy.LogicalInstallRoot) != policy.LogicalInstallRoot || policy.LogicalInstallRoot == "/" {
		return generationPolicy{}, errors.New("invalid launch policy logical install root")
	}
	return policy, nil
}

func (g generationLayout) loadGeneration(sha string) (resolvedGeneration, error) {
	if !validGenerationDigest(sha) {
		return resolvedGeneration{}, errors.New("invalid generation digest")
	}
	genDir := filepath.Join(g.generationsDir, sha)
	if err := controlledAncestors(genDir, g.anchor, g.owner); err != nil {
		return resolvedGeneration{}, err
	}
	root, err := openControlledRoot(genDir, g.anchor, g.owner)
	if err != nil {
		return resolvedGeneration{}, err
	}
	defer func() { _ = root.Close() }()
	raw, err := readControlledFile(root, "manifest.json", maxGenerationManifestBytes, g.owner)
	if err != nil {
		return resolvedGeneration{}, err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != sha {
		return resolvedGeneration{}, errors.New("generation manifest digest mismatch")
	}
	manifest, err := pkgmanifest.Read(bytes.NewReader(raw))
	if err != nil {
		return resolvedGeneration{}, err
	}
	binInfo, err := root.Lstat("bin")
	if err != nil {
		return resolvedGeneration{}, err
	}
	if _, err := controlledDir(binInfo, g.owner); err != nil {
		return resolvedGeneration{}, err
	}
	roles := make(map[string]fileFacts, 2)
	for _, role := range []string{RoleEngine, RoleDesktopApp} {
		if _, _, ok := expected(manifest, role); !ok {
			return resolvedGeneration{}, fmt.Errorf("generation manifest lacks %s", role)
		}
		info, err := root.Lstat(roleRelativePath(role))
		if err != nil {
			return resolvedGeneration{}, err
		}
		facts, err := controlledFile(info, g.owner)
		if err != nil {
			return resolvedGeneration{}, err
		}
		roles[role] = facts
	}
	return resolvedGeneration{sha: sha, manifest: manifest, roles: roles}, nil
}

func (g generationLayout) generationLocked(sha string, launcherPID int) bool {
	if !validGenerationDigest(sha) {
		return false
	}
	if err := controlledAncestors(g.locksDir, g.anchor, g.owner); err != nil {
		return false
	}
	root, err := openControlledRoot(g.locksDir, g.anchor, g.owner)
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	name := sha + ".lock"
	info, err := root.Lstat(name)
	if err != nil {
		return false
	}
	lockFacts, err := controlledLockFile(info, g.owner)
	if err != nil {
		return false
	}
	file, err := root.Open(name)
	if err == nil {
		defer func() { _ = file.Close() }()
		return sharedFlockHeld(file, g.procRoot, launcherPID, lockFacts)
	}
	if os.IsPermission(err) {
		return sharedFlockHeld(nil, g.procRoot, launcherPID, lockFacts)
	}
	return false
}

func validGenerationDigest(sha string) bool {
	if len(sha) != sha256.Size*2 || strings.ToLower(sha) != sha {
		return false
	}
	decoded, err := hex.DecodeString(sha)
	return err == nil && len(decoded) == sha256.Size
}
