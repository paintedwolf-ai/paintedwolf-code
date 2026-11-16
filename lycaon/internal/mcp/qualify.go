package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// maxQualifiedToolNameLen is the provider function-name cap (`^[a-zA-Z0-9_-]{1,64}$`).
const maxQualifiedToolNameLen = 64

// QualifiedToolName maps provider/tool to mcp_{provider}_{tool}.
// Non [a-z0-9_] runes fold to underscore; consecutive underscores collapse.
func QualifiedToolName(providerID, toolName string) string {
	provider := FoldToken(providerID)
	tool := FoldToken(toolName)
	if provider == "" {
		provider = "provider"
	}
	if tool == "" {
		tool = "tool"
	}
	name := "mcp_" + provider + "_" + tool
	return fitQualifiedToolName(name, providerID, toolName)
}

func fitQualifiedToolName(name, providerID, toolName string) string {
	if len(name) <= maxQualifiedToolNameLen {
		return name
	}
	sum := sha256.Sum256([]byte(providerID + "\x00" + toolName))
	suffix := hex.EncodeToString(sum[:4])
	keep := maxQualifiedToolNameLen - 1 - len(suffix)
	if keep < 4 {
		out := "mcp_" + suffix
		if len(out) > maxQualifiedToolNameLen {
			return out[:maxQualifiedToolNameLen]
		}
		return out
	}
	prefix := strings.TrimRight(name[:keep], "_")
	return prefix + "_" + suffix
}

// FoldToken lowercases s and folds non [a-z0-9] runes to underscore.
func FoldToken(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	b.Grow(len(s))
	lastUnderscore := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b.WriteByte(c)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

// ProviderPrefix is the qualified-name prefix every tool of a provider shares.
func ProviderPrefix(providerID string) string {
	tok := FoldToken(providerID)
	if tok == "" {
		return "mcp_"
	}
	return "mcp_" + tok + "_"
}
