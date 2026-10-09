package prompts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// PromptLayers resolves prompt assets with archive > site > project > bundled precedence.
type PromptLayers struct {
	// WorkflowArchive is the archive key of a sealed workflow version whose
	// guidance precedes every other layer.
	WorkflowArchive string
	Site            string // distribution overlay
	ProjectPrimary  string // primary project overlay
	ProjectActive   string // active project overlay
	// ModuleRoot anchors host configuration reads.
	ModuleRoot string
	// Catalog selects effective bundled units.
	Catalog *extpacks.EffectiveCatalog
	// overlaySnapshot pins overrides for one render.
	overlaySnapshot map[string][]byte
	revision        string
}

// Snapshot pins prompt sources for one render.
func (l PromptLayers) Snapshot() (PromptLayers, error) {
	if l.Catalog == nil {
		eff, err := extpacks.CatalogForConsumers()
		if err != nil {
			return PromptLayers{}, fmt.Errorf("prompt catalog: %w", err)
		}
		l.Catalog = eff
	}
	captured := make(map[string][]byte)
	for _, root := range l.roots() {
		if err := capturePromptRoot(root, captured); err != nil {
			return PromptLayers{}, err
		}
	}
	l.overlaySnapshot = captured
	l.revision = promptRevision(l.Catalog, l.WorkflowArchive, captured)
	return l, nil
}

// Revision identifies captured prompt sources.
func (l PromptLayers) Revision() string { return l.revision }

func capturePromptRoot(root string, captured map[string][]byte) error {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("prompt overlay %q: %w", root, err)
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("prompt overlay %q contains symlink %q", root, path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		ref := filepath.ToSlash(rel)
		if _, exists := captured[ref]; exists {
			return nil
		}
		data, err := (PromptLayers{}).readFileFromRoot(root, ref)
		if err != nil {
			return err
		}
		captured[ref] = append([]byte(nil), data...)
		return nil
	})
}

func promptRevision(catalog *extpacks.EffectiveCatalog, archive string, captured map[string][]byte) string {
	h := sha256.New()
	if catalog != nil {
		_, _ = io.WriteString(h, catalog.Revision)
	}
	if archive != "" {
		_, _ = io.WriteString(h, "\x00archive\x00"+archive)
	}
	keys := make([]string, 0, len(captured))
	for ref := range captured {
		keys = append(keys, ref)
	}
	sort.Strings(keys)
	for _, ref := range keys {
		_, _ = io.WriteString(h, "\x00"+ref+"\x00")
		_, _ = h.Write(captured[ref])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ProjectPromptFilesDir returns the per-project prompt overlay path.
func ProjectPromptFilesDir(projectDir string) string {
	if strings.TrimSpace(projectDir) == "" {
		return ""
	}
	return filepath.Join(filepath.Clean(projectDir), filepath.FromSlash(protectedpath.ProjectPromptFilesDir()))
}

// SitePromptFilesDir returns a checkout's distribution overlay.
func SitePromptFilesDir(configRoot string) string {
	if !configlayout.IsModuleRoot(configRoot) {
		return ""
	}
	return filepath.Join(filepath.Clean(configRoot), protectedpath.PromptFilesDirName)
}

// ReadFile returns the first matching file for ref across overlay layers.
func (l PromptLayers) ReadFile(ref string) ([]byte, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("empty template ref")
	}
	if data, ok, err := l.readArchived(ref); ok || err != nil {
		return data, err
	}
	if l.overlaySnapshot != nil {
		if data, ok := l.overlaySnapshot[ref]; ok {
			return append([]byte(nil), data...), nil
		}
		return l.readBundled(ref)
	}
	var lastErr error
	for _, root := range l.roots() {
		data, err := l.readFileFromRoot(root, ref)
		if err == nil {
			return data, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		return nil, err
	}
	if data, err := l.readBundled(ref); err == nil {
		return data, nil
	} else if errors.Is(err, ErrTemplateNotEffective) {
		// Preserve explicit disabled-unit errors.
		return nil, err
	} else if !os.IsNotExist(err) {
		lastErr = err
	}
	if lastErr != nil {
		// Preserve the bundled resolution error.
		return nil, fmt.Errorf("template %q not found in prompt overlay layers: %w", ref, lastErr)
	}
	return nil, fmt.Errorf("template %q not found", ref)
}

// readArchived returns a sealed workflow version's own guidance for ref.
func (l PromptLayers) readArchived(ref string) ([]byte, bool, error) {
	if l.WorkflowArchive == "" {
		return nil, false, nil
	}
	kind, rel, ok := kindForRef(path.Clean(ref))
	if !ok || kind != "guidance" {
		return nil, false, nil
	}
	eff := l.Catalog
	if eff == nil {
		resolved, err := extpacks.CatalogForConsumers()
		if err != nil {
			return nil, false, err
		}
		eff = resolved
	}
	data, _, found := eff.UnitContent(extpacks.ArchiveGuidanceUnitID(l.WorkflowArchive, strings.TrimSuffix(rel, ".md")))
	return data, found, nil
}

func (l PromptLayers) readBundled(ref string) ([]byte, error) {
	data, _, err := BundledLayout{Catalog: l.Catalog}.ReadBundled(ref)
	if err == nil && len(data) > pongoplain.MaxSourceBytes {
		return nil, fmt.Errorf("template %q: %w", ref, pongoplain.ErrSourceLimit)
	}
	return data, err
}

func (l PromptLayers) readFileFromRoot(root, ref string) ([]byte, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, os.ErrNotExist
	}
	path, err := validatePromptsPath(root, ref)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(filepath.Clean(root), path)
	if err != nil {
		return nil, fmt.Errorf("template path relative to prompts dir: %w", err)
	}
	file, err := fseffect.OpenRead(fseffect.Location{Root: filepath.Clean(root), Rel: rel})
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, pongoplain.MaxSourceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read template %q: %w", ref, err)
	}
	if len(data) > pongoplain.MaxSourceBytes {
		return nil, fmt.Errorf("template %q: %w", ref, pongoplain.ErrSourceLimit)
	}
	// Expand overlay placeholders in copied template overrides.
	return config.ExpandOverlayDir(data), nil
}

func (l PromptLayers) roots() []string {
	var out []string
	if site := strings.TrimSpace(l.Site); site != "" {
		out = append(out, site)
	}
	active := strings.TrimSpace(l.ProjectActive)
	primary := strings.TrimSpace(l.ProjectPrimary)
	if active != "" {
		out = append(out, active)
	}
	if primary != "" && primary != active {
		out = append(out, primary)
	}
	return out
}

func validatePromptsPath(promptsRoot, ref string) (string, error) {
	if strings.TrimSpace(promptsRoot) == "" {
		return "", fmt.Errorf("prompts directory not configured")
	}
	cleanRef := filepath.Clean(filepath.FromSlash(strings.TrimSpace(ref)))
	if cleanRef == ".." || strings.HasPrefix(cleanRef, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("template path escapes prompts dir: %q", ref)
	}
	root := filepath.Clean(promptsRoot)
	path := filepath.Join(root, cleanRef)
	rel, err := filepath.Rel(root, path)
	if err != nil || sandbox.HasParentTraversal(rel) {
		return "", fmt.Errorf("template path escapes prompts dir: %q", ref)
	}
	return path, nil
}

// ValidateArchetypePartials checks required overlay partials.
func ValidateArchetypePartials(layers PromptLayers, contract *PersonaContract) error {
	if contract == nil {
		return fmt.Errorf("persona contract required")
	}
	for archName, arch := range contract.Archetypes {
		for _, partial := range arch.RequiredPartials {
			data, err := layers.ReadFile(partial)
			if err != nil {
				return fmt.Errorf("archetype %q partial %q after overlay: %w", archName, partial, err)
			}
			if len(strings.TrimSpace(string(data))) == 0 {
				return fmt.Errorf("archetype %q partial %q is empty after overlay", archName, partial)
			}
		}
	}
	return nil
}
