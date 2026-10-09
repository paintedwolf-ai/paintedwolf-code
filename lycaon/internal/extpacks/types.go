package extpacks

// DesiredState is the merged extensions.yaml document.
type DesiredState struct {
	Format   int               `yaml:"format" json:"format"`
	Packs    []DesiredPack     `yaml:"packs" json:"packs"`
	Disabled []string          `yaml:"disabled" json:"disabled"`
	Own      map[string]string `yaml:"own" json:"own"` // unit_id → pack_id
	// Configuration stores typed per-pack values, including dormant entries for absent packs.
	Configuration map[string]map[string]any `yaml:"configuration,omitempty" json:"configuration,omitempty"`
	// Declined suppresses pack suggestions across projects on this device.
	Declined []string `yaml:"declined,omitempty" json:"declined,omitempty"`
}

// DesiredOrigin names which extensions.yaml file introduced a desired-state key.
type DesiredOrigin uint8

const (
	OriginDevice DesiredOrigin = iota + 1
	OriginProject
)

// DesiredProvenance records the source of each unit disable.
type DesiredProvenance struct {
	Disabled map[string]DesiredOrigin // unit id → who disabled it
}

// DesiredPack is one packs[] row.
type DesiredPack struct {
	ID      string `yaml:"id" json:"id"`
	Source  string `yaml:"source,omitempty" json:"source,omitempty"`
	Version string `yaml:"version,omitempty" json:"version,omitempty"`
	Ref     string `yaml:"ref,omitempty" json:"ref,omitempty"`
	// Subdir locates the member manifest within a multi-package source.
	Subdir      string `yaml:"subdir,omitempty" json:"subdir,omitempty"`
	Development bool   `yaml:"development,omitempty" json:"development,omitempty"`
	Enabled     *bool  `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// InstalledFrom is the project id that accepted this pack as a suggestion.
	InstalledFrom string `yaml:"installed_from,omitempty" json:"installed_from,omitempty"`
}

// PackKind is how a pack was obtained.
type PackKind string

const (
	PackKindStock PackKind = "stock"
	PackKindGit   PackKind = "git"
	PackKindPath  PackKind = "path" // linked absolute source (no body copy)
)

// BlockedReason explains why a pack contributed nothing.
type BlockedReason string

const (
	BlockedRequires         BlockedReason = "requires"
	BlockedRequiresScanners BlockedReason = "requires_scanners"
	BlockedCompatibility    BlockedReason = "compatibility"
	BlockedDisabled         BlockedReason = "disabled"
	BlockedInvalid          BlockedReason = "invalid"
	BlockedIntegrity        BlockedReason = "integrity"
)

// UnitStatus is the effective status of a unit id after resolve.
type UnitStatus string

const (
	UnitStatusLoaded   UnitStatus = "loaded"
	UnitStatusDisabled UnitStatus = "disabled"
	UnitStatusConflict UnitStatus = "conflict"
	UnitStatusOwned    UnitStatus = "owned"
)

// PackSummary is Extensions list row material.
type PackSummary struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	Version           string            `json:"version"`
	VersionConstraint string            `json:"version_constraint,omitempty"`
	ResolvedRevision  string            `json:"resolved_revision,omitempty"`
	Integrity         string            `json:"integrity,omitempty"`
	ExtensionAPI      string            `json:"extension_api"`
	InstallationState string            `json:"installation_state"`
	InstallationScope string            `json:"installation_scope"`
	Dependencies      map[string]string `json:"dependencies,omitempty"`
	DependencyOf      []string          `json:"dependency_of,omitempty"`
	Source            string            `json:"source,omitempty"`
	Ref               string            `json:"ref,omitempty"`
	Kind              PackKind          `json:"kind"`
	// Bundled records binary provenance required for stock authority.
	Bundled               bool          `json:"bundled,omitempty"`
	Enabled               bool          `json:"enabled"`
	Removable             bool          `json:"removable"`
	UnitCount             int           `json:"unit_count"`
	HasProfile            bool          `json:"has_profile"`
	BlockedReason         BlockedReason `json:"blocked_reason,omitempty"`
	UnmetRequiresScanners []string      `json:"unmet_requires_scanners,omitempty"`
	Contributing          bool          `json:"contributing"`
	NeedsReload           bool          `json:"needs_reload,omitempty"`
	Feature               string        `json:"feature,omitempty"`
	MetaPackIDs           []string      `json:"meta_pack_ids,omitempty"`
}

// UnitContribution retains the body for inspection.
type UnitContribution struct {
	PackID string `json:"pack_id"`
	// Path preserves bundled and on-disk unit locations.
	Path    Source `json:"path"`
	Content []byte `json:"-"`
}

// UnitEffective is Effective units row material.
type UnitEffective struct {
	ID            string             `json:"id"`
	Kind          string             `json:"kind"`
	Title         string             `json:"title,omitempty"`
	Status        UnitStatus         `json:"status"`
	WinnerPackID  string             `json:"winner_pack_id,omitempty"`
	Contributions []UnitContribution `json:"contributions"`
	Content       []byte             `json:"-"` // winner body when loaded/owned
}

// Diagnostic is a catalog resolution note.
type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Severity is derived from Code, never authored, and stamped once when a
	// catalog is finalized.
	Severity      DiagnosticSeverity `json:"severity"`
	UnitID        string             `json:"unit_id,omitempty"`
	PackID        string             `json:"pack_id,omitempty"`
	MCPProviderID string             `json:"mcp_provider_id,omitempty"`
	ScannerID     string             `json:"scanner_id,omitempty"`
}

// DiagnosticSeverity reflects whether the requested catalog state took effect.
type DiagnosticSeverity string

const (
	// SeverityError: something the desired state asked for is not in effect.
	SeverityError DiagnosticSeverity = "error"
	// SeverityWarning: in effect, but not as written — a refused line, an
	// environment that supplies nothing, a stale link.
	SeverityWarning DiagnosticSeverity = "warning"
	// SeverityInfo: the catalog reporting a choice back.
	SeverityInfo DiagnosticSeverity = "info"
)

// diagnosticSeverity classifies every code in the registry. An unlisted code
// reads as an error rather than being quietly downgraded.
var diagnosticSeverity = map[string]DiagnosticSeverity{
	// Asked for, not in effect.
	DiagExtensionAPIIncompatible:  SeverityError,
	DiagRequiresUnmet:             SeverityError,
	DiagPackInvalid:               SeverityError,
	DiagPackIntegrity:             SeverityError,
	DiagConflict:                  SeverityError,
	DiagPlatformMissing:           SeverityError,
	DiagMCPBindingInvalid:         SeverityError,
	DiagCredentialSlotsInvalid:    SeverityError,
	DiagMCPBindingDupKey:          SeverityError,
	DiagToolSchemaInvalid:         SeverityError,
	DiagToolSchemaMissing:         SeverityError,
	DiagApprovalRuleInvalid:       SeverityError,
	DiagPathMissing:               SeverityError,
	DiagDesiredStateRejected:      SeverityError,
	DiagDetectionPackInvalid:      SeverityError,
	DiagDetectionPackForeignUnit:  SeverityError,
	DiagDetectionPackIDTaken:      SeverityError,
	DiagDetectionRehearsalFailed:  SeverityError,
	DiagUnitNotRegular:            SeverityError,
	DiagSkillNameInvalid:          SeverityError,
	DiagSkillNameMismatch:         SeverityError,
	DiagSkillFrontmatterInvalid:   SeverityError,
	DiagSkillFieldMissing:         SeverityError,
	DiagSkillFieldInvalid:         SeverityError,
	DiagSkillCompatibilityInvalid: SeverityError,
	DiagSkillTooLarge:             SeverityError,
	DiagSkillHostResourcesInvalid: SeverityError,
	DiagSkillTemplateInvalid:      SeverityError,
	DiagMetaInvalid:               SeverityError,
	DiagMetaCompatibility:         SeverityError,
	DiagMetaDuplicateID:           SeverityError,
	DiagSuiteConflict:             SeverityError,
	DiagMemberMissing:             SeverityError,
	DiagExtendsUnresolved:         SeverityError,
	DiagExtendsCoResolve:          SeverityError,
	// Refused project units fail authoring validation.
	DiagProjectScopeRefused: SeverityError,

	// In effect, but not as written.
	DiagRequiresScannersUnmet:   SeverityWarning,
	DiagPackNeedsReload:         SeverityWarning,
	DiagOwnUnknownPack:          SeverityWarning,
	DiagOwnRefused:              SeverityWarning,
	DiagProjectTrustOff:         SeverityWarning,
	DiagSkillCatalogFull:        SeverityWarning,
	DiagSkillShadowed:           SeverityWarning,
	DiagSkillHostResourcesUnmet: SeverityWarning,
	DiagMemberDisabled:          SeverityWarning,

	// Choices reported back.
	DiagPackDisabled:         SeverityInfo,
	DiagUnitDisabled:         SeverityInfo,
	DiagOwned:                SeverityInfo,
	DiagProjectPackSuggested: SeverityInfo,
}

// SeverityForCode returns the severity a diagnostic code carries.
func SeverityForCode(code string) DiagnosticSeverity {
	if severity, ok := diagnosticSeverity[code]; ok {
		return severity
	}
	return SeverityError
}

// StampSeverities fills in the severity of every diagnostic, at the one place a
// catalog or report is finalized.
func StampSeverities(diags []Diagnostic) []Diagnostic {
	for i := range diags {
		diags[i].Severity = SeverityForCode(diags[i].Code)
	}
	return diags
}

// ErrorDiagnosticCount counts diagnostics saying something asked for is not in
// effect.
func ErrorDiagnosticCount(diags []Diagnostic) int {
	n := 0
	for _, d := range diags {
		if SeverityForCode(d.Code) == SeverityError {
			n++
		}
	}
	return n
}

// HasErrorDiagnostic reports whether anything asked for is not in effect.
func HasErrorDiagnostic(diags []Diagnostic) bool { return ErrorDiagnosticCount(diags) > 0 }

// Diagnostic codes are stable catalog identifiers.
const (
	DiagExtensionAPIIncompatible  = "extension_api_incompatible"
	DiagRequiresUnmet             = "requires_unmet"
	DiagRequiresScannersUnmet     = "requires_scanners_unmet"
	DiagPackDisabled              = "pack_disabled"
	DiagPackInvalid               = "pack_invalid"
	DiagPackIntegrity             = "pack_integrity"
	DiagPackNeedsReload           = "pack_needs_reload"
	DiagUnitDisabled              = "unit_disabled"
	DiagConflict                  = "conflict"
	DiagOwned                     = "owned"
	DiagOwnUnknownPack            = "own_unknown_pack"
	DiagPlatformMissing           = "platform_missing"
	DiagMCPBindingInvalid         = "mcp_binding_invalid"
	DiagCredentialSlotsInvalid    = "credential_slots_invalid"
	DiagMCPBindingDupKey          = "mcp_binding_duplicate_key"
	DiagToolSchemaInvalid         = "tool_schema_invalid"
	DiagToolSchemaMissing         = "tool_schema_missing"
	DiagApprovalRuleInvalid       = "approval_rule_invalid"
	DiagPathMissing               = "path_missing"
	DiagSkillNameInvalid          = "skill_name_invalid"
	DiagSkillNameMismatch         = "skill_name_mismatch"
	DiagSkillFrontmatterInvalid   = "skill_frontmatter_invalid"
	DiagSkillFieldMissing         = "skill_field_missing"
	DiagSkillFieldInvalid         = "skill_field_invalid"
	DiagSkillCompatibilityInvalid = "skill_compatibility_invalid"
	DiagSkillTooLarge             = "skill_too_large"
	DiagSkillTemplateInvalid      = "skill_template_invalid"
	DiagSkillCatalogFull          = "skill_catalog_full"
	DiagSkillShadowed             = "skill_shadowed"
	DiagSkillHostResourcesInvalid = "skill_host_resources_invalid"
	DiagSkillHostResourcesUnmet   = "skill_host_resources_unmet"
	DiagProjectScopeRefused       = "project_scope_refused"
	DiagDesiredStateRejected      = "desired_state_rejected"
	DiagOwnRefused                = "own_refused"
	DiagDetectionPackInvalid      = "detection_pack_invalid"
	DiagDetectionPackForeignUnit  = "detection_pack_foreign_unit"
	DiagDetectionPackIDTaken      = "detection_pack_id_taken"
	DiagProjectTrustOff           = "project_trust_off"
	DiagDetectionRehearsalFailed  = "detection_rehearsal_failed"
	DiagProjectPackSuggested      = "project_pack_suggested"
)

// allDiagnosticCodes is the complete catalog diagnostic registry.
var allDiagnosticCodes = []string{
	DiagExtensionAPIIncompatible,
	DiagRequiresUnmet,
	DiagRequiresScannersUnmet,
	DiagPackDisabled,
	DiagPackInvalid,
	DiagPackIntegrity,
	DiagPackNeedsReload,
	DiagUnitDisabled,
	DiagConflict,
	DiagOwned,
	DiagOwnUnknownPack,
	DiagPlatformMissing,
	DiagMCPBindingInvalid,
	DiagCredentialSlotsInvalid,
	DiagMCPBindingDupKey,
	DiagToolSchemaInvalid,
	DiagToolSchemaMissing,
	DiagApprovalRuleInvalid,
	DiagPathMissing,
	DiagSkillNameInvalid,
	DiagSkillNameMismatch,
	DiagSkillFrontmatterInvalid,
	DiagSkillFieldMissing,
	DiagSkillFieldInvalid,
	DiagSkillCompatibilityInvalid,
	DiagSkillTooLarge,
	DiagSkillTemplateInvalid,
	DiagSkillCatalogFull,
	DiagSkillShadowed,
	DiagSkillHostResourcesInvalid,
	DiagSkillHostResourcesUnmet,
	DiagProjectScopeRefused,
	DiagDesiredStateRejected,
	DiagOwnRefused,
	DiagDetectionPackInvalid,
	DiagDetectionPackForeignUnit,
	DiagDetectionPackIDTaken,
	DiagProjectTrustOff,
	DiagDetectionRehearsalFailed,
	DiagProjectPackSuggested,
	DiagUnitNotRegular,
	DiagMemberMissing,
	DiagMemberDisabled,
	DiagSuiteConflict,
	DiagMetaCompatibility,
	DiagMetaInvalid,
	DiagMetaDuplicateID,
	DiagExtendsUnresolved,
	DiagExtendsCoResolve,
}

// AllDiagnosticCodes returns a copy of the resolve diagnostic code registry.
func AllDiagnosticCodes() []string {
	return append([]string(nil), allDiagnosticCodes...)
}

// Manifest is extension.yaml (author-facing).
type Manifest struct {
	ManifestVersion  int                          `yaml:"manifest_version"`
	ID               string                       `yaml:"id"`
	Name             string                       `yaml:"name"`
	Description      string                       `yaml:"description,omitempty"`
	Version          string                       `yaml:"version"`
	Compatibility    ManifestCompatibility        `yaml:"compatibility"`
	Dependencies     map[string]DependencyRequest `yaml:"dependencies,omitempty"`
	RequiresScanners []string                     `yaml:"requires_scanners"`
	Feature          string                       `yaml:"feature"`
}

// ManifestCompatibility states which extension host contract a release uses.
type ManifestCompatibility struct {
	ExtensionAPI         string   `yaml:"extension_api"`
	RequiresCapabilities []string `yaml:"requires_capabilities,omitempty"`
}

// DependencyRequest is one package constraint and its authoritative source.
type DependencyRequest struct {
	Source  string `yaml:"source"`
	Version string `yaml:"version"`
}

// PackContent is a discovered pack plus inventoried units.
// Diagnostics retains discovery refusals for resolution.
type PackContent struct {
	Pack        Pack
	Manifest    Manifest
	Kind        PackKind
	Locked      *LockedPackage
	Units       []InventoriedUnit
	Diagnostics []Diagnostic
	// NeedsReload: path bytes differ from the lock; current bytes still contribute.
	NeedsReload bool
	// OmitReason: listed, contributes nothing.
	OmitReason BlockedReason
	OmitDetail string
}

// InventoriedUnit is one provide candidate from a pack.
type InventoriedUnit struct {
	ID      string // e.g. policy/WRITE_SCOPE_DENIED, workflows/plan, guidance/coordinator-gate-blocked
	Kind    string // policy | guidance | workflows | …
	Path    Source // where the unit's bytes live
	Content []byte
}
