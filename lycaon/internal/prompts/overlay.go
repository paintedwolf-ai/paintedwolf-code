package prompts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
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
	WorkflowArchive string // sealed workflow archive copy (highest precedence)
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
	l.revision = promptRevision(l.Catalog, captured)
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

func promptRevision(catalog *extpacks.EffectiveCatalog, captured map[string][]byte) string {
	h := sha256.New()
	if catalog != nil {
		_, _ = io.WriteString(h, catalog.Revision)
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

// UnitProvenanceRecord captures resolved tier, location, and hash of a template or partial.
type UnitProvenanceRecord struct {
	UnitKind      string `json:"unit_kind"`
	UnitID        string `json:"unit_id"`
	SourceTier    string `json:"source_tier"`
	SourcePath    string `json:"source_path"`
	ContentSha256 string `json:"content_sha256"`
}

type rootTier struct {
	root string
	tier string
}

func (l PromptLayers) rootsWithTiers() []rootTier {
	var out []rootTier
	if archive := strings.TrimSpace(l.WorkflowArchive); archive != "" {
		out = append(out, rootTier{root: archive, tier: "archive"})
	}
	if site := strings.TrimSpace(l.Site); site != "" {
		out = append(out, rootTier{root: site, tier: "site"})
	}
	active := strings.TrimSpace(l.ProjectActive)
	primary := strings.TrimSpace(l.ProjectPrimary)
	if active != "" {
		out = append(out, rootTier{root: active, tier: "project"})
	}
	if primary != "" && primary != active {
		out = append(out, rootTier{root: primary, tier: "project"})
	}
	return out
}

// ReadFile returns the first matching file for ref across overlay layers.
func (l PromptLayers) ReadFile(ref string) ([]byte, error) {
	data, _, err := l.ReadFileWithProvenance(ref)
	return data, err
}

// ReadFileWithProvenance returns the first matching file for ref across overlay layers,
// along with its provenance details.
func (l PromptLayers) ReadFileWithProvenance(ref string) ([]byte, UnitProvenanceRecord, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, UnitProvenanceRecord{}, fmt.Errorf("empty template ref")
	}
	kind, _, ok := kindForRef(ref)
	if !ok {
		kind = "prompt"
	}
	if l.overlaySnapshot != nil {
		if data, ok := l.overlaySnapshot[ref]; ok {
			sum := sha256.Sum256(data)
			return append([]byte(nil), data...), UnitProvenanceRecord{
				UnitKind:      kind,
				UnitID:        ref,
				SourceTier:    "snapshot",
				SourcePath:    ref,
				ContentSha256: hex.EncodeToString(sum[:]),
			}, nil
		}
		return l.readBundledWithProvenance(ref)
	}
	var lastErr error
	for _, rt := range l.rootsWithTiers() {
		data, resolvedPath, err := l.readFileFromRootWithPath(rt.root, ref)
		if err == nil {
			sum := sha256.Sum256(data)
			return data, UnitProvenanceRecord{
				UnitKind:      kind,
				UnitID:        ref,
				SourceTier:    rt.tier,
				SourcePath:    resolvedPath,
				ContentSha256: hex.EncodeToString(sum[:]),
			}, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		return nil, UnitProvenanceRecord{}, err
	}
	data, prov, err := l.readBundledWithProvenance(ref)
	if err == nil {
		return data, prov, nil
	} else if errors.Is(err, ErrTemplateNotEffective) {
		// Preserve explicit disabled-unit errors.
		return nil, UnitProvenanceRecord{}, err
	} else if !os.IsNotExist(err) {
		lastErr = err
	}
	if lastErr != nil {
		// Preserve the bundled resolution error.
		return nil, UnitProvenanceRecord{}, fmt.Errorf("template %q not found in prompt overlay layers: %w", ref, lastErr)
	}
	return nil, UnitProvenanceRecord{}, fmt.Errorf("template %q not found", ref)
}

func (l PromptLayers) readBundled(ref string) ([]byte, error) {
	data, _, err := l.readBundledWithProvenance(ref)
	return data, err
}

func (l PromptLayers) readBundledWithProvenance(ref string) ([]byte, UnitProvenanceRecord, error) {
	data, src, err := BundledLayout{Catalog: l.Catalog}.ReadBundled(ref)
	if err != nil {
		return nil, UnitProvenanceRecord{}, err
	}
	if len(data) > pongoplain.MaxSourceBytes {
		return nil, UnitProvenanceRecord{}, fmt.Errorf("template %q: %w", ref, pongoplain.ErrSourceLimit)
	}
	kind, _, ok := kindForRef(ref)
	if !ok {
		kind = "prompt"
	}
	sum := sha256.Sum256(data)
	return data, UnitProvenanceRecord{
		UnitKind:      kind,
		UnitID:        ref,
		SourceTier:    "bundled",
		SourcePath:    src.String(),
		ContentSha256: hex.EncodeToString(sum[:]),
	}, nil
}

func (l PromptLayers) readFileFromRoot(root, ref string) ([]byte, error) {
	data, _, err := l.readFileFromRootWithPath(root, ref)
	return data, err
}

func (l PromptLayers) readFileFromRootWithPath(root, ref string) ([]byte, string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, "", os.ErrNotExist
	}
	data, path, err := l.readFromPathWithPath(root, ref)
	if err == nil {
		return data, path, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	if kind, rel, ok := kindForRef(ref); ok {
		candidate := kind + "/" + rel
		if candidate != ref {
			if candData, candPath, candErr := l.readFromPathWithPath(root, candidate); candErr == nil {
				return candData, candPath, nil
			}
		}
	}
	return nil, "", os.ErrNotExist
}

func (l PromptLayers) readFromPath(root, ref string) ([]byte, error) {
	data, _, err := l.readFromPathWithPath(root, ref)
	return data, err
}

func (l PromptLayers) readFromPathWithPath(root, ref string) ([]byte, string, error) {
	path, err := validatePromptsPath(root, ref)
	if err != nil {
		return nil, "", err
	}
	rel, err := filepath.Rel(filepath.Clean(root), path)
	if err != nil {
		return nil, "", fmt.Errorf("template path relative to prompts dir: %w", err)
	}
	file, err := fseffect.OpenRead(fseffect.Location{Root: filepath.Clean(root), Rel: rel})
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, pongoplain.MaxSourceBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read template %q: %w", ref, err)
	}
	if len(data) > pongoplain.MaxSourceBytes {
		return nil, "", fmt.Errorf("template %q: %w", ref, pongoplain.ErrSourceLimit)
	}
	// Expand overlay placeholders in copied template overrides.
	return config.ExpandOverlayDir(data), path, nil
}

func (l PromptLayers) roots() []string {
	var out []string
	for _, rt := range l.rootsWithTiers() {
		out = append(out, rt.root)
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
