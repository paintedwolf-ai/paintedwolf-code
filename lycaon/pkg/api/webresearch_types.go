package api

// WebResearchProviderKind classifies a catalog provider configuration shape.
type WebResearchProviderKind string

const (
	WebResearchProviderKindKeyed           WebResearchProviderKind = "keyed"
	WebResearchProviderKindKeyedExtra      WebResearchProviderKind = "keyed_extra"
	WebResearchProviderKindKeylessEndpoint WebResearchProviderKind = "keyless_endpoint"
	WebResearchProviderKindKeyless         WebResearchProviderKind = "keyless"
)

// WebResearchProviderRole is a catalog provider participation axis.
type WebResearchProviderRole string

const (
	WebResearchProviderRoleResults WebResearchProviderRole = "results"
	WebResearchProviderRoleSeeds   WebResearchProviderRole = "seeds"
)
