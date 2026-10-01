package mcp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const producesEvidenceMetaKey = "produces_evidence"

// ProducesEvidenceDecl is optional MCP tool metadata that upgrades opaque capture.
type ProducesEvidenceDecl struct {
	Kind  string `json:"kind"`
	Shape string `json:"shape"`
}

// ProducesEvidenceFromTool parses optional produces_evidence from MCP tool _meta.
func ProducesEvidenceFromTool(tool *sdkmcp.Tool) (ProducesEvidenceDecl, bool) {
	if tool == nil || len(tool.Meta) == 0 {
		return ProducesEvidenceDecl{}, false
	}
	raw, ok := tool.Meta[producesEvidenceMetaKey]
	if !ok {
		return ProducesEvidenceDecl{}, false
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return ProducesEvidenceDecl{}, false
	}
	var decl ProducesEvidenceDecl
	if err := json.Unmarshal(data, &decl); err != nil {
		return ProducesEvidenceDecl{}, false
	}
	decl.Kind = strings.TrimSpace(decl.Kind)
	decl.Shape = strings.TrimSpace(decl.Shape)
	if decl.Kind == "" {
		return ProducesEvidenceDecl{}, false
	}
	return decl, true
}

// EvidenceDeclarationFromTool builds one optional discovery declaration.
func EvidenceDeclarationFromTool(qualifiedTool string, tool *sdkmcp.Tool) (*evidence.MCPToolDeclaration, error) {
	decl, ok := ProducesEvidenceFromTool(tool)
	if !ok {
		return nil, nil
	}
	normalized, err := evidence.NewMCPToolDeclaration(qualifiedTool, decl.Kind, decl.Shape)
	if err != nil {
		return nil, fmt.Errorf("mcp tool %q evidence: %w", qualifiedTool, err)
	}
	return &normalized, nil
}
