// Package approvalregistry loads the per-tool approval explanation registry directory.
package approvalregistry

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"gopkg.in/yaml.v3"
)

// DefaultDir is the platform pack approval registry, for callers that load that
// one directory rather than the ListEffective union of pack approvals/ dirs.
const DefaultDir = config.PlatformApprovals

// ExplainManifestName is the filename inside each approvals/<KEY>/ feature dir.
const ExplainManifestName = "explain.yaml"

// Entry is one approval explanation YAML file, bundled or on a host overlay.
type Entry struct {
	Key  string
	Path extpacks.Source
	Data []byte
}

// ListEffective unions approvals/ across every contributing pack — stock and
// installed — through the process catalog (resolving committed device state
// when none is active yet).
func ListEffective() ([]Entry, error) {
	catalog, err := extpacks.CatalogForConsumers()
	if err != nil {
		return nil, err
	}
	return ListEffectiveWithCatalog(catalog)
}

// ListEffectiveWithCatalog compiles approval explanations from the resolved
// winners' captured bytes (unit ids approvals/<KEY>/explain); the view never
// rereads pack files after resolve.
func ListEffectiveWithCatalog(catalog *extpacks.EffectiveCatalog) ([]Entry, error) {
	if catalog == nil {
		return nil, fmt.Errorf("approval registry: effective catalog required")
	}
	seen := map[string]extpacks.Source{}
	var out []Entry
	for _, id := range catalog.LoadedUnitIDs() {
		key, ok := approvalKeyForUnitID(id)
		if !ok {
			continue
		}
		content, _, ok := catalog.UnitContent(id)
		if !ok {
			continue
		}
		at, _ := catalog.UnitPath(id)
		gotKey, err := keyFromFile(at, key, content)
		if err != nil {
			return nil, err
		}
		// One winner per unit id after resolve, so a duplicate key here means
		// two different unit ids claim the same approval key.
		if prev, dup := seen[gotKey]; dup {
			return nil, fmt.Errorf("approval %q in both %s and %s", gotKey, prev, at)
		}
		seen[gotKey] = at
		out = append(out, Entry{Key: gotKey, Path: at, Data: content})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("extpacks: no approval explanation entries in effective packs")
	}
	return out, nil
}

// approvalKeyForUnitID maps approvals/<KEY>/explain to <KEY>. Any other shape
// under approvals/ is not an explanation unit.
func approvalKeyForUnitID(id string) (string, bool) {
	parts := strings.Split(id, "/")
	if len(parts) != 3 || parts[0] != "approvals" || parts[2] != "explain" || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

// List reads and validates all explain.yaml files under dir — a bundled pack
// directory or a host directory (a project overlay).
func List(dir extpacks.Source) ([]Entry, error) {
	info, err := dir.Stat()
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s: approval registry path must be a directory", dir)
	}
	names, err := dir.List()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Entry
	for _, ent := range names {
		if !ent.IsDir() {
			continue
		}
		path := dir.Join(ent.Name(), ExplainManifestName)
		data, err := path.Read()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		key, err := keyFromFile(path, ent.Name(), data)
		if err != nil {
			return nil, err
		}
		if key != ent.Name() {
			return nil, fmt.Errorf("%s: directory name must match explanation key %q", path, key)
		}
		if seen[key] {
			return nil, fmt.Errorf("%s: duplicate explanation key %q", path, key)
		}
		seen[key] = true
		out = append(out, Entry{Key: key, Path: path, Data: data})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no approval explanation entries found", dir)
	}
	return out, nil
}

// WriteFile writes body to path with mode 0600.
func WriteFile(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o600) // #nosec G306 -- config artifact in repo
}

// Prune removes feature dirs not listed in keepKeys.
func Prune(dir string, keepKeys map[string]bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		if keepKeys[ent.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, ent.Name())); err != nil {
			return err
		}
	}
	return nil
}

func keyFromFile(path extpacks.Source, dirname string, data []byte) (string, error) {
	var fragment struct {
		Explanations map[string]any `yaml:"approval_explanations"`
	}
	if err := yaml.Unmarshal(data, &fragment); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if len(fragment.Explanations) != 1 {
		return "", fmt.Errorf("%s: expected exactly one approval_explanations entry", path)
	}
	for key := range fragment.Explanations {
		if key != dirname {
			return "", fmt.Errorf("%s: approval_explanations key must match directory %q", path, dirname)
		}
		return key, nil
	}
	return "", fmt.Errorf("%s: approval_explanations entry required", path)
}
