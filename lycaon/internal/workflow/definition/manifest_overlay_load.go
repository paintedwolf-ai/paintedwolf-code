package definition

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

func ResolveAllManifests(raw map[string]Manifest) (map[string]Manifest, error) {
	resolved := make(map[string]Manifest, len(raw))
	for key, m := range raw {
		eff, err := ResolveManifestChain(m, raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		if err := ValidatePhaseTargets(eff); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		resolved[key] = eff
	}
	if err := validateUniqueSessionCreateAttach(resolved); err != nil {
		return nil, err
	}
	if err := validateWorkflowInvocations(resolved); err != nil {
		return nil, err
	}
	for key, manifest := range resolved {
		if manifest.Request == nil {
			return nil, fmt.Errorf("%s: workflow manifest %s: request required", key, manifest.ID)
		}
	}
	return resolved, nil
}

func LoadManifestsRaw(dir string) (map[string]Manifest, error) {
	entries, _, err := loadManifestsRawWithSources(dir, OriginDisk)
	return entries, err
}

func loadManifestsRawWithSources(dir string, origin ManifestSourceOrigin) (map[string]Manifest, map[string]ManifestSource, error) {
	entries := map[string]Manifest{}
	sources := map[string]ManifestSource{}
	if dir == "" {
		return entries, sources, nil
	}
	// Workflow directories share the <name>/workflow.yaml layout.
	featureDirs, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	for _, ent := range featureDirs {
		if !ent.IsDir() || strings.HasPrefix(ent.Name(), "_") {
			continue
		}
		p := filepath.Join(dir, ent.Name(), "workflow.yaml")
		if _, err := os.Stat(p); err != nil {
			continue
		}
		m, err := LoadManifestFromFile(p)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", p, err)
		}
		key := ManifestKey(m.ID, m.Version)
		if _, dup := entries[key]; dup {
			return nil, nil, fmt.Errorf("duplicate workflow manifest %s", key)
		}
		entries[key] = m
		sources[key] = ManifestSource{Key: key, Path: p, Origin: origin}
	}
	return entries, sources, nil
}

// MergeManifestOverlay overlays project manifests onto bundled entries (project wins per id@version).
func MergeManifestOverlay(bundled map[string]Manifest, projectDir string) (map[string]Manifest, error) {
	mergedRaw := map[string]Manifest{}
	for k, v := range bundled {
		mergedRaw[k] = v
	}
	if strings.TrimSpace(projectDir) == "" {
		return ResolveAllManifests(mergedRaw)
	}
	overlayDir := filepath.Join(projectDir, settingsoverlay.DirName(), "workflows")
	info, err := os.Stat(overlayDir)
	if err != nil {
		if os.IsNotExist(err) {
			return ResolveAllManifests(mergedRaw)
		}
		return nil, err
	}
	if !info.IsDir() {
		return ResolveAllManifests(mergedRaw)
	}
	overlayRaw, err := LoadManifestsRaw(overlayDir)
	if err != nil {
		return nil, err
	}
	for k, v := range overlayRaw {
		mergedRaw[k] = v
	}
	return ResolveAllManifests(mergedRaw)
}

// ManifestSourceOrigin records where a resolved manifest was authored.
type ManifestSourceOrigin int

const (
	OriginBundled ManifestSourceOrigin = iota
	OriginDisk
	OriginOverlay
)

// ManifestSource ties a registry key to its authored path.
type ManifestSource struct {
	Key    string
	Path   string
	Origin ManifestSourceOrigin
}

// RegistryFromDirs loads workflows with an optional project overlay.
func RegistryFromDirs(projectDir string) (*Registry, error) {
	reg, _, err := RegistryFromDirsWithSources(projectDir)
	if err != nil {
		return nil, err
	}
	// Follow effective catalog generations.
	reg.derive = func() (map[string]Manifest, error) {
		next, _, err := RegistryFromDirsWithSources(projectDir)
		if err != nil {
			return nil, err
		}
		return next.All(), nil
	}
	reg.revision = activeCatalogRevision()
	return reg, nil
}

// RegistryFromDirsWithSources is RegistryFromDirs plus a source index keyed by id@version.
func RegistryFromDirsWithSources(projectDir string) (*Registry, map[string]ManifestSource, error) {
	// Catalog bytes already include device-scope pack resolution.
	mergedRaw, sources, err := LoadPackManifestsForCatalog(nil)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(projectDir) != "" {
		overlayDir := filepath.Join(projectDir, settingsoverlay.DirName(), "workflows")
		if info, err := os.Stat(overlayDir); err == nil && info.IsDir() {
			overlayRaw, overlaySources, err := loadManifestsRawWithSources(overlayDir, OriginOverlay)
			if err != nil {
				return nil, nil, err
			}
			for k, v := range overlayRaw {
				mergedRaw[k] = v
				sources[k] = overlaySources[k]
			}
		} else if err != nil && !os.IsNotExist(err) {
			return nil, nil, err
		}
	}
	resolved, err := ResolveAllManifests(mergedRaw)
	if err != nil {
		return nil, nil, err
	}
	if ref, cfgErr := LoadRegistryConfig(extpacks.Bundled(config.PlatformFlows)); cfgErr == nil {
		if err := ValidateAmbientAttachBijection(resolved, ref); err != nil {
			return nil, nil, err
		}
	}
	return NewRegistry(resolved), sources, nil
}
