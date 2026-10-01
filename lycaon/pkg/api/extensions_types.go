package api

// Extension wire types for Settings → Extensions.

// ExtensionsDesiredScope is project vs device desired-state writes.
type ExtensionsDesiredScope string

const (
	ExtensionsScopeProject ExtensionsDesiredScope = "project"
	ExtensionsScopeDevice  ExtensionsDesiredScope = "device"
)

// ExtensionPackKind is how a pack was obtained.
type ExtensionPackKind string

const (
	ExtensionPackKindStock ExtensionPackKind = "stock"
	ExtensionPackKindGit   ExtensionPackKind = "git"
	ExtensionPackKindPath  ExtensionPackKind = "path"
)

// ExtensionMetaPackStatus is derived suite status.
type ExtensionMetaPackStatus string

const (
	ExtensionMetaPackComplete ExtensionMetaPackStatus = "complete"
	ExtensionMetaPackPartial  ExtensionMetaPackStatus = "partial"
	ExtensionMetaPackInactive ExtensionMetaPackStatus = "inactive"
)

// ExtensionBlockedReason explains why a pack did not contribute.
type ExtensionBlockedReason string

const (
	ExtensionBlockedRequires         ExtensionBlockedReason = "requires"
	ExtensionBlockedRequiresScanners ExtensionBlockedReason = "requires_scanners"
	ExtensionBlockedCompatibility    ExtensionBlockedReason = "compatibility"
	ExtensionBlockedDisabled         ExtensionBlockedReason = "disabled"
	ExtensionBlockedInvalid          ExtensionBlockedReason = "invalid"
	ExtensionBlockedIntegrity        ExtensionBlockedReason = "integrity"
)

// ExtensionUnitStatus is the effective status of a unit id.
type ExtensionUnitStatus string

const (
	ExtensionUnitStatusLoaded   ExtensionUnitStatus = "loaded"
	ExtensionUnitStatusDisabled ExtensionUnitStatus = "disabled"
	ExtensionUnitStatusConflict ExtensionUnitStatus = "conflict"
	ExtensionUnitStatusOwned    ExtensionUnitStatus = "owned"
)

// ExtensionDiagnosticSeverity is how a resolve diagnostic should be read.
type ExtensionDiagnosticSeverity string

const (
	// ExtensionDiagnosticError: content asked for is not in effect.
	ExtensionDiagnosticError ExtensionDiagnosticSeverity = "error"
	// ExtensionDiagnosticWarning: in effect, but not as written.
	ExtensionDiagnosticWarning ExtensionDiagnosticSeverity = "warning"
	// ExtensionDiagnosticInfo: a choice reported back.
	ExtensionDiagnosticInfo ExtensionDiagnosticSeverity = "info"
)

// ExtensionSuggestionStatus is whether a proposal is still on offer.
type ExtensionSuggestionStatus string

const (
	ExtensionSuggestionAvailable       ExtensionSuggestionStatus = "available"
	ExtensionSuggestionInstalled       ExtensionSuggestionStatus = "installed"
	ExtensionSuggestionDeclined        ExtensionSuggestionStatus = "declined"
	ExtensionSuggestionVersionMismatch ExtensionSuggestionStatus = "version_mismatch"
)
