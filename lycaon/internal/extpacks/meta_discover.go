package extpacks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"gopkg.in/yaml.v3"
)

// Suite relationships reference packs and suites, not units.
var unitKindMemberPrefixes = []string{
	"policy/", "guidance/", "workflows/", "agents/", "tools/",
	"approvals/", "playbooks/", "shared/", "host/", "mcp_bindings/", "scanners/",
}

// StockMetaPath is the bundled stock suite manifest.
func StockMetaPath() config.Rel { return config.StockMeta }

// MetaPackCacheRoot is {configdir}/extensions-meta.
func MetaPackCacheRoot() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, enginepaths.ExtensionsMetaDirName), nil
}

// CachedMetaPackDir is {configdir}/extensions-meta/<pack-id-safe>.
func CachedMetaPackDir(metaPackID string) (string, error) {
	if err := ValidatePackID(metaPackID); err != nil {
		return "", err
	}
	root, err := MetaPackCacheRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, PackIDSafe(metaPackID)), nil
}

// MetaPackMetadata identifies one cached suite.
type MetaPackMetadata struct {
	MetaPackID       string   `json:"meta_pack_id"`
	Version          string   `json:"version"`
	Source           string   `json:"source"`
	Ref              string   `json:"ref,omitempty"`
	ResolvedRevision string   `json:"resolved_revision,omitempty"`
	Integrity        string   `json:"integrity"`
	ExtensionAPI     string   `json:"extension_api"`
	MemberPackIDs    []string `json:"member_pack_ids"`
	Kind             PackKind `json:"kind"`
}

func WriteMetaPackMetadata(metaRoot string, meta MetaPackMetadata) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return writeCacheMetadataFile(filepath.Join(metaRoot, MetaPackMetadataName), append(data, '\n'))
}

func ReadMetaPackMetadata(metaRoot string) (MetaPackMetadata, error) {
	data, err := os.ReadFile(filepath.Join(metaRoot, MetaPackMetadataName))
	if err != nil {
		return MetaPackMetadata{}, err
	}
	var meta MetaPackMetadata
	if err := decodeStrictMetadata(data, &meta); err != nil {
		return MetaPackMetadata{}, fmt.Errorf("suite metadata: %w", err)
	}
	return meta, nil
}

// DiscoverMetaPacks inventories the stock suite plus {configdir}/extensions-meta.
// err is only for fatal I/O; per-manifest problems become diags and are omitted.
func DiscoverMetaPacks() (metas []MetaPack, diags []Diagnostic, err error) {
	byID := map[string]MetaPack{}
	order := []string{}

	keep := func(m MetaPack) {
		id := m.Manifest.ID
		if prev, ok := byID[id]; ok {
			if IsStockMetaPackID(id) && (prev.Kind == PackKindStock || m.Kind == PackKindStock) {
				if m.Kind == PackKindStock {
					byID[id] = m
				}
				diags = append(diags, Diagnostic{
					Code: DiagMetaDuplicateID, PackID: id,
					Message: fmt.Sprintf("duplicate meta-pack id %q; keeping stock", id),
				})
				return
			}
			diags = append(diags, Diagnostic{
				Code: DiagMetaDuplicateID, PackID: id,
				Message: fmt.Sprintf("duplicate meta-pack id %q; keeping first (%s)", id, prev.Root),
			})
			return
		}
		byID[id] = m
		order = append(order, id)
	}

	if stock, stockDiags, stockErr := loadStockMetaPack(); stockErr != nil {
		return nil, nil, stockErr
	} else if stock != nil {
		keep(*stock)
		diags = append(diags, stockDiags...)
	} else {
		diags = append(diags, stockDiags...)
	}

	cacheMetas, cacheDiags, cacheErr := loadCachedMetaPacks()
	if cacheErr != nil {
		return nil, nil, cacheErr
	}
	diags = append(diags, cacheDiags...)
	sort.Slice(cacheMetas, func(i, j int) bool { return cacheMetas[i].Root < cacheMetas[j].Root })
	for _, m := range cacheMetas {
		keep(m)
	}

	out := make([]MetaPack, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if IsStockMetaPackID(out[i].Manifest.ID) {
			return !IsStockMetaPackID(out[j].Manifest.ID)
		}
		if IsStockMetaPackID(out[j].Manifest.ID) {
			return false
		}
		return out[i].Manifest.ID < out[j].Manifest.ID
	})
	return out, diags, nil
}

func loadStockMetaPack() (*MetaPack, []Diagnostic, error) {
	path := StockMetaPath()
	data, err := config.Read(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("extpacks: read stock meta: %w", err)
	}
	man, diags := parseMetaPackManifest(data, path.String())
	if len(diags) > 0 {
		return nil, diags, nil
	}
	return &MetaPack{Manifest: man, Kind: PackKindStock, Root: config.StockPacks.String()}, nil, nil
}

func loadCachedMetaPacks() ([]MetaPack, []Diagnostic, error) {
	root, err := MetaPackCacheRoot()
	if err != nil {
		return nil, nil, err
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var out []MetaPack
	var diags []Diagnostic
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		metaRoot := filepath.Join(root, ent.Name())
		path := filepath.Join(metaRoot, MetaPackFileName)
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			diags = append(diags, Diagnostic{
				Code: DiagMetaInvalid, PackID: ent.Name(),
				Message: fmt.Sprintf("read %s: %v", path, err),
			})
			continue
		}
		man, parseDiags := parseMetaPackManifest(data, path)
		if len(parseDiags) > 0 {
			diags = append(diags, parseDiags...)
			continue
		}
		meta, metaErr := ReadMetaPackMetadata(metaRoot)
		if metaErr != nil || ValidatePackID(meta.MetaPackID) != nil || meta.MetaPackID != man.ID || PackIDSafe(man.ID) != ent.Name() {
			diags = append(diags, Diagnostic{Code: DiagMetaInvalid, PackID: man.ID, Message: "cached suite provenance does not match meta.yaml"})
			continue
		}
		integrity, integrityErr := PackTreeIntegrity(metaRoot)
		if integrityErr != nil || meta.Version != man.Version || meta.ExtensionAPI != man.Compatibility.ExtensionAPI ||
			meta.Integrity != integrity || !slices.Equal(meta.MemberPackIDs, man.Members) {
			diags = append(diags, Diagnostic{Code: DiagMetaInvalid, PackID: man.ID, Message: "cached suite metadata or integrity does not match meta.yaml"})
			continue
		}
		kind := meta.Kind
		if kind != PackKindGit && kind != PackKindPath {
			diags = append(diags, Diagnostic{Code: DiagMetaInvalid, PackID: man.ID, Message: "cached suite kind is invalid"})
			continue
		}
		out = append(out, MetaPack{Manifest: man, Kind: kind, Root: metaRoot})
	}
	return out, diags, nil
}

// parseMetaPackManifest returns diagnostics for invalid manifests.
func parseMetaPackManifest(data []byte, source string) (MetaPackManifest, []Diagnostic) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var man MetaPackManifest
	if err := dec.Decode(&man); err != nil {
		return MetaPackManifest{}, []Diagnostic{{
			Code: DiagMetaInvalid, Message: fmt.Sprintf("%s: %v", source, err),
		}}
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("multiple YAML documents are not allowed")
		}
		return MetaPackManifest{}, []Diagnostic{{
			Code: DiagMetaInvalid, Message: fmt.Sprintf("%s: %v", source, err),
		}}
	}
	if err := validateMetaPackManifest(&man); err != nil {
		code := DiagMetaInvalid
		if errors.Is(err, ErrExtensionAPICompatibility) {
			code = DiagMetaCompatibility
		}
		return MetaPackManifest{}, []Diagnostic{{
			Code: code, PackID: strings.TrimSpace(man.ID),
			Message: fmt.Sprintf("%s: %v", source, err),
		}}
	}
	normalizeMetaPackManifest(&man)
	return man, nil
}

func validateMetaPackManifest(man *MetaPackManifest) error {
	id := strings.TrimSpace(man.ID)
	name := strings.TrimSpace(man.Name)
	version := strings.TrimSpace(man.Version)
	if id == "" {
		return fmt.Errorf("id is required")
	}
	if err := ValidatePackID(id); err != nil {
		return fmt.Errorf("id: %w", err)
	}
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if man.ManifestVersion != ManifestVersion {
		return fmt.Errorf("unsupported manifest_version %d (want %d)", man.ManifestVersion, ManifestVersion)
	}
	if _, err := parseCanonicalVersion(version); err != nil {
		return fmt.Errorf("version: %w", err)
	}
	if _, err := semver.NewConstraint(strings.TrimSpace(man.Compatibility.ExtensionAPI)); err != nil {
		return fmt.Errorf("%w: compatibility.extension_api: %w", ErrExtensionAPICompatibility, err)
	}
	if !ManifestHostCompatible(Manifest{Compatibility: man.Compatibility}) {
		return fmt.Errorf("%w: compatibility.extension_api %q does not include host %s",
			ErrExtensionAPICompatibility, man.Compatibility.ExtensionAPI, ExtensionAPIVersion)
	}
	if len(man.Members) == 0 {
		return fmt.Errorf("members must be non-empty")
	}
	seen := map[string]struct{}{}
	for _, m := range man.Members {
		m = strings.TrimSpace(m)
		if m == "" {
			return fmt.Errorf("members contains empty id")
		}
		if err := ValidatePackID(m); err != nil {
			return fmt.Errorf("member %q: %w", m, err)
		}
		if looksLikeUnitID(m) {
			return fmt.Errorf("member %q looks like a unit id, not a pack id", m)
		}
		if m == id {
			return fmt.Errorf("members must not include self id %q", id)
		}
		if _, ok := seen[m]; ok {
			return fmt.Errorf("duplicate member %q", m)
		}
		seen[m] = struct{}{}
	}
	conflictsSeen := map[string]struct{}{}
	for _, c := range man.ConflictsWith {
		c = strings.TrimSpace(c)
		if c == "" {
			return fmt.Errorf("conflicts_with contains empty id")
		}
		if err := ValidatePackID(c); err != nil {
			return fmt.Errorf("conflicts_with %q: %w", c, err)
		}
		if looksLikeUnitID(c) {
			return fmt.Errorf("conflicts_with %q looks like a unit id", c)
		}
		if c == id {
			return fmt.Errorf("conflicts_with must not include self id")
		}
		if _, ok := seen[c]; ok {
			return fmt.Errorf("conflicts_with %q is a pack member id, not a meta-pack id", c)
		}
		if _, ok := conflictsSeen[c]; ok {
			return fmt.Errorf("duplicate conflicts_with %q", c)
		}
		conflictsSeen[c] = struct{}{}
	}
	extendsSeen := map[string]struct{}{}
	for _, e := range man.Extends {
		e = strings.TrimSpace(e)
		if e == "" {
			return fmt.Errorf("extends contains empty id")
		}
		if err := ValidatePackID(e); err != nil {
			return fmt.Errorf("extends %q: %w", e, err)
		}
		if looksLikeUnitID(e) {
			return fmt.Errorf("extends %q looks like a unit id", e)
		}
		if e == id {
			return fmt.Errorf("extends must not include self id")
		}
		if _, ok := seen[e]; ok {
			return fmt.Errorf("extends %q is a pack member id, not a meta-pack id", e)
		}
		if _, ok := extendsSeen[e]; ok {
			return fmt.Errorf("duplicate extends %q", e)
		}
		if _, ok := conflictsSeen[e]; ok {
			return fmt.Errorf("meta-pack %q cannot be both extended and conflicting", e)
		}
		extendsSeen[e] = struct{}{}
	}
	return nil
}

func normalizeMetaPackManifest(man *MetaPackManifest) {
	man.ID = strings.TrimSpace(man.ID)
	man.Name = strings.TrimSpace(man.Name)
	man.Version = strings.TrimSpace(man.Version)
	man.Compatibility.ExtensionAPI = strings.TrimSpace(man.Compatibility.ExtensionAPI)
	man.Members = trimSortUnique(man.Members)
	man.ConflictsWith = trimSortUnique(man.ConflictsWith)
	man.Extends = trimSortUnique(man.Extends)
}

func trimSortUnique(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func looksLikeUnitID(id string) bool {
	for _, p := range unitKindMemberPrefixes {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}
