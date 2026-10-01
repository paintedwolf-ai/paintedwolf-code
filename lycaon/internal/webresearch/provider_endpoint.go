package webresearch

import "strings"

// keylessEndpoint returns the configured base URL for a provider that needs no
// API key, with any trailing slash trimmed.
func keylessEndpoint(s Settings, providerID string) string {
	return strings.TrimSuffix(strings.TrimSpace(s.Config[providerID]["endpoint"]), "/")
}

// politeContactMailto identifies this client to APIs that request contact details.
const politeContactMailto = "search@paint-wolf-code.com"
