package observation

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
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

func normalizeCompletenessValue(c AttributionCompleteness) AttributionCompleteness {
	if c == "partial_kernel_cached" {
		return CompletenessPartialKernelCached
	}
	return c
}

// NormalizeVisibilityFacts synchronizes Mode/UntouchedVisibility, Completeness/AttributionCompleteness,
// and invalidation capability fields on VisibilityFacts without inventing verified status.
func NormalizeVisibilityFacts(facts VisibilityFacts) VisibilityFacts {
	if facts.UntouchedVisibility == "" && facts.Mode != "" {
		facts.UntouchedVisibility = facts.Mode
	}
	if facts.Mode == "" && facts.UntouchedVisibility != "" {
		facts.Mode = facts.UntouchedVisibility
	}
	facts.Completeness = normalizeCompletenessValue(facts.Completeness)
	facts.AttributionCompleteness = normalizeCompletenessValue(facts.AttributionCompleteness)
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
		facts.InvalidationVerified = facts.InvalidationVerified && facts.SupportsInvalidation
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
// Capability support is distinct from verified invalidation (InvalidationVerified defaults to false).
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
		completeness = CompletenessUnknown
	}
	return NormalizeVisibilityFacts(VisibilityFacts{
		AdapterName:                        adapterName,
		Mode:                               VisibilityModeLiveMount,
		UntouchedVisibility:                VisibilityLive,
		LiveVisible:                        true,
		SupportsInvalidation:               invalidationUpcall,
		SupportsInvalidationUpcall:         invalidationUpcall,
		SupportsNegativeLookupInvalidation: invalidationUpcall && negativeLookupInvalidation,
		SupportsDirCookieInvalidation:      invalidationUpcall && dirCookieInvalidation,
		InvalidationVerified:               false,
		Completeness:                       completeness,
		AttributionCompleteness:            completeness,
	})
}

// MaterializedSeedVisibilityFacts returns explicit visibility facts for materialized fallback views.
func MaterializedSeedVisibilityFacts(adapterName string) VisibilityFacts {
	if strings.TrimSpace(adapterName) == "" {
		adapterName = string(adapter.AdapterEagerCopy)
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

// EagerCopyViewCapabilities returns a ViewCapabilityDescriptor from EagerCopyAdapter's typed facts.
func EagerCopyViewCapabilities(viewID string) ViewCapabilityDescriptor {
	return ViewCapabilitiesFromAdapterFacts(viewID, adapter.NewEagerCopyAdapter(adapter.ReasonNone).Facts())
}

// MountedAdapterViewCapabilities returns a ViewCapabilityDescriptor for a mounted adapter.
func MountedAdapterViewCapabilities(
	viewID string,
	adapterName string,
	supportsInvalidation bool,
	negativeLookupInvalidation bool,
	dirCookieInvalidation bool,
	completeness AttributionCompleteness,
) ViewCapabilityDescriptor {
	if completeness == "" {
		completeness = CompletenessUnknown
	}
	return ViewCapabilityDescriptor{
		ViewID:                             viewID,
		AdapterName:                        adapterName,
		PreparationKind:                    string(adapter.PreparationMounted),
		Mode:                               VisibilityModeLiveMount,
		UntouchedVisibility:                VisibilityLive,
		LiveVisible:                        true,
		SupportsInvalidation:               supportsInvalidation,
		SupportsInvalidationUpcall:         supportsInvalidation,
		SupportsNegativeLookupInvalidation: supportsInvalidation && negativeLookupInvalidation,
		SupportsDirCookieInvalidation:      supportsInvalidation && dirCookieInvalidation,
		InvalidationVerified:               false,
		Completeness:                       completeness,
		AttributionCompleteness:            completeness,
	}
}

// ViewCapabilitiesFromAdapterFacts converts shared adapter.AdapterFacts (#1658) into a ViewCapabilityDescriptor.
func ViewCapabilitiesFromAdapterFacts(viewID string, facts adapter.AdapterFacts) ViewCapabilityDescriptor {
	var mode UntouchedVisibilityMode
	switch facts.UntouchedSourceVisibility {
	case adapter.VisibilityLive:
		mode = VisibilityLive
	case adapter.VisibilityFixed:
		mode = VisibilityFixed
	default:
		mode = VisibilityUnknown
	}
	completeness := normalizeCompletenessValue(AttributionCompleteness(strings.TrimSpace(facts.AttributionCompleteness)))
	if completeness == "" {
		completeness = CompletenessUnknown
	}
	invalidation := mode == VisibilityLive && facts.CacheRevalidation.ExplicitInvalidation
	verified := invalidation && facts.CacheRevalidation.InvalidationVerified
	return ViewCapabilityDescriptor{
		ViewID:                             strings.TrimSpace(viewID),
		AdapterName:                        string(facts.AdapterID),
		PreparationKind:                    string(facts.PreparationKind),
		Mode:                               mode,
		UntouchedVisibility:                mode,
		LiveVisible:                        mode == VisibilityLive,
		SupportsInvalidation:               invalidation,
		SupportsInvalidationUpcall:         invalidation,
		SupportsNegativeLookupInvalidation: invalidation && facts.CacheRevalidation.NegativeLookupInvalidation,
		SupportsDirCookieInvalidation:      invalidation && facts.CacheRevalidation.DirCookieInvalidation,
		InvalidationVerified:               verified,
		Completeness:                       completeness,
		AttributionCompleteness:            completeness,
	}
}

// ViewCapabilitiesFromPreparedView extracts the ViewCapabilityDescriptor from a prepared adapter.PreparedView (#1658).
func ViewCapabilitiesFromPreparedView(view *adapter.PreparedView) ViewCapabilityDescriptor {
	if view == nil {
		return ViewCapabilityDescriptor{}
	}
	return ViewCapabilitiesFromAdapterFacts(view.ViewID, view.Facts)
}

// VisibilityFactsFromDescriptor constructs VisibilityFacts strictly from explicit typed binding fields,
// never inferring visibility from adapter-name spelling or upgrading unknown completeness/verification.
func VisibilityFactsFromDescriptor(desc ViewCapabilityDescriptor) VisibilityFacts {
	adapterName := strings.TrimSpace(desc.AdapterName)
	mode := desc.UntouchedVisibility
	if mode == "" {
		mode = desc.Mode
	}
	if mode == "" {
		switch adapter.PreparationKind(strings.ToLower(strings.TrimSpace(desc.PreparationKind))) {
		case adapter.PreparationMaterialized:
			mode = VisibilityFixed
		case adapter.PreparationMounted, "mount":
			mode = VisibilityLive
		default:
			if desc.LiveVisible {
				mode = VisibilityLive
			} else {
				mode = VisibilityUnknown
			}
		}
	}
	completeness := normalizeCompletenessValue(desc.AttributionCompleteness)
	if completeness == "" {
		completeness = normalizeCompletenessValue(desc.Completeness)
	}
	if completeness == "" {
		completeness = CompletenessUnknown
	}

	switch mode {
	case VisibilityFixed:
		if adapterName == "" {
			adapterName = string(adapter.AdapterEagerCopy)
		}
		return NormalizeVisibilityFacts(VisibilityFacts{
			AdapterName:             adapterName,
			Mode:                    VisibilityModeMaterializedSeed,
			UntouchedVisibility:     VisibilityFixed,
			Completeness:            completeness,
			AttributionCompleteness: completeness,
		})
	case VisibilityLive:
		if adapterName == "" {
			adapterName = "live-mount"
		}
		invalidation := desc.SupportsInvalidation || desc.SupportsInvalidationUpcall
		verified := desc.InvalidationVerified && invalidation
		return NormalizeVisibilityFacts(VisibilityFacts{
			AdapterName:                        adapterName,
			Mode:                               VisibilityModeLiveMount,
			UntouchedVisibility:                VisibilityLive,
			LiveVisible:                        true,
			SupportsInvalidation:               invalidation,
			SupportsInvalidationUpcall:         invalidation,
			SupportsNegativeLookupInvalidation: invalidation && desc.SupportsNegativeLookupInvalidation,
			SupportsDirCookieInvalidation:      invalidation && desc.SupportsDirCookieInvalidation,
			InvalidationVerified:               verified,
			Completeness:                       completeness,
			AttributionCompleteness:            completeness,
		})
	default:
		return UnknownVisibilityFacts(adapterName)
	}
}
