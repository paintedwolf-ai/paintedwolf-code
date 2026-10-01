package mcp

import (
	"strings"
	"unicode"
)

// CredentialWire is how a stored static token is placed on the HTTP request.
type CredentialWire string

const (
	CredentialWireBearer     CredentialWire = "bearer"
	CredentialWireTokenToken CredentialWire = "token_token"
	CredentialWireHeader     CredentialWire = "header"
)

// NormalizeCredentialWire maps empty to bearer. ok is false for an unknown value.
func NormalizeCredentialWire(raw string) (CredentialWire, bool) {
	switch CredentialWire(strings.TrimSpace(raw)) {
	case "", CredentialWireBearer:
		return CredentialWireBearer, true
	case CredentialWireTokenToken:
		return CredentialWireTokenToken, true
	case CredentialWireHeader:
		return CredentialWireHeader, true
	default:
		return "", false
	}
}

// formatStaticCredential returns the header name and value for a stored token.
// Unknown wire, or header placement without a name, yields empty strings.
func formatStaticCredential(wire, header, secret string) (name, value string) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return "", ""
	}
	kind, ok := NormalizeCredentialWire(wire)
	if !ok {
		return "", ""
	}
	switch kind {
	case CredentialWireTokenToken:
		return "Authorization", "Token token=" + secret
	case CredentialWireHeader:
		name = strings.TrimSpace(header)
		if !validCredentialHeader(name) {
			return "", ""
		}
		return name, secret
	default:
		return "Authorization", "Bearer " + secret
	}
}

func validCredentialHeader(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !isHTTPTokenRune(r) {
			return false
		}
	}
	return true
}

func isHTTPTokenRune(r rune) bool {
	if r > unicode.MaxASCII {
		return false
	}
	switch r {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	}
	return (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
}

func validateCredentialPlacement(wire, header string) string {
	kind, ok := NormalizeCredentialWire(wire)
	if !ok {
		return RejectInvalidEntry
	}
	if kind == CredentialWireHeader && !validCredentialHeader(strings.TrimSpace(header)) {
		return RejectInvalidEntry
	}
	return ""
}
