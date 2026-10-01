package llm

// EndpointStyle describes the provider-specific meaning of base_url.
type EndpointStyle string

const (
	EndpointStyleURL     EndpointStyle = "url"
	EndpointStyleRegion  EndpointStyle = "region"
	EndpointStyleDerived EndpointStyle = "derived"
)

// Normalize applies the catalog default.
func (s EndpointStyle) Normalize() EndpointStyle {
	if s == "" {
		return EndpointStyleURL
	}
	return s
}

// Validate rejects unsupported catalog values.
func (s EndpointStyle) Validate() bool {
	switch s.Normalize() {
	case EndpointStyleURL, EndpointStyleRegion, EndpointStyleDerived:
		return true
	default:
		return false
	}
}
