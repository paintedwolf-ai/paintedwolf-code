package observation

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

// ViewCapabilityDescriptor binds a workspace view to its visibility facts.
type ViewCapabilityDescriptor struct {
	ViewID string          `json:"view_id,omitempty"`
	Facts  VisibilityFacts `json:"facts"`
}

// ValidateVisibilityFacts refuses undeclared values and invalidation claims on a view that is not live.
func ValidateVisibilityFacts(facts VisibilityFacts) error {
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
	invalidation := facts.SupportsInvalidation || facts.SupportsNegativeLookupInvalidation || facts.SupportsDirCookieInvalidation || facts.InvalidationVerified
	if invalidation && !facts.Live() {
		return fmt.Errorf("%w: invalidation declared for a %s view", ErrInvalidVisibilityFacts, facts.UntouchedVisibility)
	}
	if !facts.SupportsInvalidation && (facts.SupportsNegativeLookupInvalidation || facts.SupportsDirCookieInvalidation || facts.InvalidationVerified) {
		return fmt.Errorf("%w: invalidation detail declared without invalidation support", ErrInvalidVisibilityFacts)
	}
	return nil
}

// UnknownVisibilityFacts returns conservative unverified facts for a tracker with no bound view.
func UnknownVisibilityFacts(adapterName string) VisibilityFacts {
	if strings.TrimSpace(adapterName) == "" {
		adapterName = "unbound"
	}
	return VisibilityFacts{AdapterName: adapterName, UntouchedVisibility: VisibilityUnknown, AttributionCompleteness: CompletenessUnknown}
}

// VisibilityFactsFromAdapter derives a view's visibility facts from its adapter's declared facts.
func VisibilityFactsFromAdapter(facts adapter.AdapterFacts) VisibilityFacts {
	out := VisibilityFacts{
		AdapterName:             string(facts.AdapterID),
		UntouchedVisibility:     VisibilityUnknown,
		AttributionCompleteness: AttributionCompleteness(strings.TrimSpace(facts.AttributionCompleteness)),
	}
	switch facts.UntouchedSourceVisibility {
	case adapter.VisibilityLive:
		out.UntouchedVisibility = VisibilityLive
		reval := facts.CacheRevalidation
		out.SupportsInvalidation = reval.ExplicitInvalidation
		out.SupportsNegativeLookupInvalidation = reval.ExplicitInvalidation && reval.NegativeLookupInvalidation
		out.SupportsDirCookieInvalidation = reval.ExplicitInvalidation && reval.DirCookieInvalidation
		out.InvalidationVerified = reval.ExplicitInvalidation && reval.InvalidationVerified
	case adapter.VisibilityFixed:
		out.UntouchedVisibility = VisibilityFixed
	}
	if out.AttributionCompleteness == "" {
		out.AttributionCompleteness = CompletenessUnknown
	}
	return out
}

// ViewCapabilitiesFromAdapterFacts binds viewID to the facts its adapter declares.
func ViewCapabilitiesFromAdapterFacts(viewID string, facts adapter.AdapterFacts) ViewCapabilityDescriptor {
	return ViewCapabilityDescriptor{ViewID: strings.TrimSpace(viewID), Facts: VisibilityFactsFromAdapter(facts)}
}

// ViewCapabilitiesFromPreparedView binds a prepared view to its adapter's facts.
func ViewCapabilitiesFromPreparedView(view *adapter.PreparedView) ViewCapabilityDescriptor {
	if view == nil {
		return ViewCapabilityDescriptor{}
	}
	return ViewCapabilitiesFromAdapterFacts(view.ViewID, view.Facts)
}
