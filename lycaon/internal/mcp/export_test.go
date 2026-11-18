package mcp

// ProviderPrefix is the qualified-name prefix every tool of a provider shares.
func ProviderPrefix(providerID string) string {
	tok := FoldToken(providerID)
	if tok == "" {
		return "mcp_"
	}
	return "mcp_" + tok + "_"
}
