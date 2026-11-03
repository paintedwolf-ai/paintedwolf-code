package observation

import (
	"fmt"
	"strings"
)

// ViewCapabilityDescriptor describes the selected workspace view adapter and its
// declared visibility, cache invalidation, and read-attribution capabilities.
type ViewCapabilityDescriptor struct {
	ViewID                             string                  `json:"view_id,omitempty"`
	AdapterName                        string                  `json:"adapter_name"`
	PreparationKind                    string                  `json:"preparation_kind,omitempty"`
	Mode                               UntouchedVisibilityMode `json:"mode,omitempty"`
	UntouchedVisibility                UntouchedVisibilityMode `json:"untouched_visibility,omitempty"`
	LiveVisible                        bool                    `json:"live_visible"`
	SupportsInvalidation               bool                    `json:"supports_invalidation"`
	SupportsInvalidationUpcall         bool                    `json:"supports_invalidation_upcall"`
	SupportsNegativeLookupInvalidation bool                    `json:"supports_negative_lookup_invalidation"`
	SupportsDirCookieInvalidation      bool                    `json:"supports_dir_cookie_invalidation"`
	InvalidationVerified               bool                    `json:"invalidation_verified"`
	Completeness                       AttributionCompleteness `json:"completeness,omitempty"`
	AttributionCompleteness            AttributionCompleteness `json:"attribution_completeness,omitempty"`
}

// NormalizeVisibilityFacts synchronizes Mode/UntouchedVisibility, Completeness/AttributionCompleteness,
// and invalidation capability fields on VisibilityFacts.
func NormalizeVisibilityFacts(facts VisibilityFacts) VisibilityFacts {
	if facts.UntouchedVisibility == "" && facts.Mode != "" {
		facts.UntouchedVisibility = facts.Mode
	}
	if facts.Mode == "" && facts.UntouchedVisibility != "" {
		facts.Mode = facts.UntouchedVisibility
	}
	if facts.AttributionCompleteness == "" && facts.Completeness != "" {
		facts.AttributionCompleteness = facts.Completeness
	}
	if facts.Completeness == "" && facts.AttributionCompleteness != "" {
		facts.Completeness = facts.AttributionCompleteness
	}
	if facts.SupportsInvalidationUpcall && !facts.SupportsInvalidation {
		facts.SupportsInvalidation = true
	}
	if facts.SupportsInvalidation && !facts.SupportsInvalidationUpcall {
		facts.SupportsInvalidationUpcall = true
	}
	if facts.UntouchedVisibility == VisibilityLive {
		facts.LiveVisible = true
		if facts.SupportsInvalidation && !facts.InvalidationVerified {
			facts.InvalidationVerified = true
		}
	} else {
		facts.LiveVisible = false
		facts.SupportsInvalidation = false
		facts.SupportsInvalidationUpcall = false
		facts.SupportsNegativeLookupInvalidation = false
		facts.SupportsDirCookieInvalidation = false
		facts.InvalidationVerified = false
	}
	return facts
}

// ValidateVisibilityFacts verifies that adapter visibility facts are explicitly declared.
func ValidateVisibilityFacts(facts VisibilityFacts) error {
	facts = NormalizeVisibilityFacts(facts)
	switch facts.UntouchedVisibility {
	case VisibilityLive, VisibilityFixed, VisibilityUnknown:
	default:
		return fmt.Errorf("%w: unsupported untouched_visibility %q", ErrInvalidVisibilityFacts, facts.UntouchedVisibility)
	}
	switch facts.AttributionCompleteness {
	case CompletenessComplete, CompletenessPartialKernelCached, CompletenessMaterializedSnapshot, CompletenessUnknown:
	default:
		return fmt.Errorf("%w: unsupported attribution_completeness %q", ErrInvalidVisibilityFacts, facts.AttributionCompleteness)
	}
	return nil
}

// LiveMountVisibilityFacts returns explicit visibility facts for a live mounted adapter.
func LiveMountVisibilityFacts(
	adapterName string,
	invalidationUpcall bool,
	negativeLookupInvalidation bool,
	dirCookieInvalidation bool,
	completeness AttributionCompleteness,
) VisibilityFacts {
	if strings.TrimSpace(adapterName) == "" {
		adapterName = "live-mount"
	}
	if completeness == "" {
		completeness = CompletenessPartialKernelCached
	}
	return NormalizeVisibilityFacts(VisibilityFacts{
		AdapterName:                        adapterName,
		Mode:                               VisibilityModeLiveMount,
		UntouchedVisibility:                VisibilityLive,
		LiveVisible:                        true,
		SupportsInvalidation:               invalidationUpcall,
		SupportsInvalidationUpcall:         invalidationUpcall,
		SupportsNegativeLookupInvalidation: negativeLookupInvalidation,
		SupportsDirCookieInvalidation:      dirCookieInvalidation,
		InvalidationVerified:               invalidationUpcall,
		Completeness:                       completeness,
		AttributionCompleteness:            completeness,
	})
}

// MaterializedSeedVisibilityFacts returns explicit visibility facts for materialized fallback views.
func MaterializedSeedVisibilityFacts(adapterName string) VisibilityFacts {
	if strings.TrimSpace(adapterName) == "" {
		adapterName = "eagercopy"
	}
	return NormalizeVisibilityFacts(VisibilityFacts{
		AdapterName:                        adapterName,
		Mode:                               VisibilityModeMaterializedSeed,
		UntouchedVisibility:                VisibilityFixed,
		LiveVisible:                        false,
		SupportsInvalidation:               false,
		SupportsInvalidationUpcall:         false,
		SupportsNegativeLookupInvalidation: false,
		SupportsDirCookieInvalidation:      false,
		InvalidationVerified:               false,
		Completeness:                       CompletenessMaterializedSnapshot,
		AttributionCompleteness:            CompletenessMaterializedSnapshot,
	})
}

// UnknownVisibilityFacts returns conservative unverified visibility facts for an unbound tracker.
func UnknownVisibilityFacts(adapterName string) VisibilityFacts {
	if strings.TrimSpace(adapterName) == "" {
		adapterName = "unbound"
	}
	return NormalizeVisibilityFacts(VisibilityFacts{
		AdapterName:                        adapterName,
		Mode:                               VisibilityModeUnknown,
		UntouchedVisibility:                VisibilityUnknown,
		LiveVisible:                        false,
		SupportsInvalidation:               false,
		SupportsInvalidationUpcall:         false,
		SupportsNegativeLookupInvalidation: false,
		SupportsDirCookieInvalidation:      false,
		InvalidationVerified:               false,
		Completeness:                       CompletenessUnknown,
		AttributionCompleteness:            CompletenessUnknown,
	})
}

// EagerCopyViewCapabilities returns a ViewCapabilityDescriptor for an eagercopy materialized fallback view.
func EagerCopyViewCapabilities(viewID string) ViewCapabilityDescriptor {
	return ViewCapabilityDescriptor{
		ViewID:                  viewID,
		AdapterName:             "eagercopy",
		PreparationKind:         "materialized",
		Mode:                    VisibilityModeMaterializedSeed,
		UntouchedVisibility:     VisibilityFixed,
		LiveVisible:             false,
		SupportsInvalidation:    false,
		InvalidationVerified:    false,
		Completeness:            CompletenessMaterializedSnapshot,
		AttributionCompleteness: CompletenessMaterializedSnapshot,
	}
}

// MountedAdapterViewCapabilities returns a ViewCapabilityDescriptor for a mounted adapter (e.g., fuse or nfs).
func MountedAdapterViewCapabilities(
	viewID string,
	adapterName string,
	supportsInvalidation bool,
	negativeLookupInvalidation bool,
	dirCookieInvalidation bool,
	completeness AttributionCompleteness,
) ViewCapabilityDescriptor {
	if completeness == "" {
		completeness = CompletenessPartialKernelCached
	}
	return ViewCapabilityDescriptor{
		ViewID:                             viewID,
		AdapterName:                        adapterName,
		PreparationKind:                    "mount",
		Mode:                               VisibilityModeLiveMount,
		UntouchedVisibility:                VisibilityLive,
		LiveVisible:                        true,
		SupportsInvalidation:               supportsInvalidation,
		SupportsInvalidationUpcall:         supportsInvalidation,
		SupportsNegativeLookupInvalidation: negativeLookupInvalidation,
		SupportsDirCookieInvalidation:      dirCookieInvalidation,
		InvalidationVerified:               supportsInvalidation,
		Completeness:                       completeness,
		AttributionCompleteness:            completeness,
	}
}

// VisibilityFactsFromDescriptor constructs VisibilityFacts from a bound ViewCapabilityDescriptor.
func VisibilityFactsFromDescriptor(desc ViewCapabilityDescriptor) VisibilityFacts {
	adapterName := strings.TrimSpace(desc.AdapterName)
	mode := desc.Mode
	if mode == "" {
		mode = desc.UntouchedVisibility
	}
	completeness := desc.Completeness
	if completeness == "" {
		completeness = desc.AttributionCompleteness
	}
	prep := strings.ToLower(strings.TrimSpace(desc.PreparationKind))
	lowerAdapter := strings.ToLower(adapterName)

	isMaterialized := mode == VisibilityModeMaterializedSeed ||
		prep == "materialized" ||
		prep == "eagercopy" ||
		lowerAdapter == "eagercopy" ||
		lowerAdapter == "materialized-fallback" ||
		completeness == CompletenessMaterializedSnapshot

	if isMaterialized {
		facts := MaterializedSeedVisibilityFacts(adapterName)
		if completeness == CompletenessPartialKernelCached || completeness == CompletenessMaterializedSnapshot {
			facts.Completeness = completeness
			facts.AttributionCompleteness = completeness
		}
		return facts
	}

	isLiveMount := mode == VisibilityModeLiveMount ||
		desc.LiveVisible ||
		prep == "mount" ||
		strings.Contains(lowerAdapter, "fuse") ||
		strings.Contains(lowerAdapter, "nfs") ||
		strings.Contains(lowerAdapter, "fskit")

	if isLiveMount {
		if completeness == "" || completeness == CompletenessUnknown {
			completeness = CompletenessPartialKernelCached
		}
		invalidation := desc.SupportsInvalidation || desc.SupportsInvalidationUpcall
		facts := LiveMountVisibilityFacts(
			adapterName,
			invalidation,
			desc.SupportsNegativeLookupInvalidation,
			desc.SupportsDirCookieInvalidation,
			completeness,
		)
		facts.InvalidationVerified = desc.InvalidationVerified || invalidation
		return facts
	}

	return UnknownVisibilityFacts(adapterName)
}
