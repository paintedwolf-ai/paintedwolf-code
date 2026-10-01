package extpacks

import "strings"

// StockMetaPackID is the single Painted Wolf stock suite id.
const StockMetaPackID = "painted-wolf/stock"

// MetaPackFileName is the suite membership manifest filename.
const MetaPackFileName = "meta.yaml"

const MetaPackMetadataName = ".suite-metadata.json"

// MetaPackStatus is derived suite status from member pack enablement.
type MetaPackStatus string

const (
	MetaPackComplete MetaPackStatus = "complete"
	MetaPackPartial  MetaPackStatus = "partial"
	MetaPackInactive MetaPackStatus = "inactive"
)

// Suite diagnostics are shared by validation and settings.
const (
	DiagMemberMissing     = "member_missing"
	DiagMemberDisabled    = "member_disabled"
	DiagSuiteConflict     = "suite_conflict"
	DiagMetaCompatibility = "meta_compatibility"
	DiagMetaInvalid       = "meta_invalid"
	DiagMetaDuplicateID   = "meta_duplicate_id"
	DiagExtendsUnresolved = "extends_unresolved"
	DiagExtendsCoResolve  = "extends_co_resolve"
)

// MetaPackManifest is meta.yaml (author-facing).
type MetaPackManifest struct {
	ManifestVersion int                   `yaml:"manifest_version"`
	ID              string                `yaml:"id"`
	Name            string                `yaml:"name"`
	Version         string                `yaml:"version"`
	Compatibility   ManifestCompatibility `yaml:"compatibility"`
	Members         []string              `yaml:"members"`
	ConflictsWith   []string              `yaml:"conflicts_with"`
	Extends         []string              `yaml:"extends"`
}

// MetaPack is a discovered suite membership root.
type MetaPack struct {
	Manifest MetaPackManifest
	Kind     PackKind // stock | git | path
	Root     string   // directory containing meta.yaml
}

// MetaPackSummary is Extensions / validate suite row material.
type MetaPackSummary struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Version       string         `json:"version"`
	Kind          PackKind       `json:"kind"`
	Status        MetaPackStatus `json:"status"`
	Members       []string       `json:"members"`
	ConflictsWith []string       `json:"conflicts_with"`
	Extends       []string       `json:"extends"`
	Diagnostics   []Diagnostic   `json:"diagnostics,omitempty"`
	Removable     bool           `json:"removable"`
}

// IsStockMetaPackID reports whether id is the stock suite.
func IsStockMetaPackID(id string) bool {
	return strings.TrimSpace(id) == StockMetaPackID
}
