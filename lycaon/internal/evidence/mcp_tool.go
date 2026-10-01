package evidence

import (
	"strings"
	"unicode"

	"github.com/lycaon/lycaon/internal/ingestion"
)

// MCPServerKindPrefix separates server-defined and host-defined evidence kinds.
const MCPServerKindPrefix = "mcp_server_"

// IsMCPServerKind reports whether kind was contributed by an MCP server.
func IsMCPServerKind(kind string) bool {
	return strings.HasPrefix(strings.TrimSpace(kind), MCPServerKindPrefix)
}

// NamespaceMCPServerKind qualifies a server-defined evidence kind.
func NamespaceMCPServerKind(kind string) string {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return ""
	}
	if IsMCPServerKind(kind) {
		return kind
	}
	return MCPServerKindPrefix + kind
}

// MCPEvidenceKindFromTool derives a collision-safe kind from a qualified tool name.
func MCPEvidenceKindFromTool(qualifiedTool string) string {
	q := strings.TrimPrefix(normalizeToolName(qualifiedTool), ingestion.MCPToolPrefix)
	var b strings.Builder
	for _, r := range q {
		if unicode.IsLetter(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	if b.Len() == 0 {
		return MCPServerKindPrefix + "mcp"
	}
	return MCPServerKindPrefix + b.String()
}
