package fileoutline

import "github.com/lycaon/lycaon/internal/syntaxhealth"

// SyntaxDiagnostic is a compact, one-based source location for outline responses.
type SyntaxDiagnostic struct {
	Row      int    `json:"row"`
	Col      int    `json:"col"`
	Snippet  string `json:"snippet"`
	Kind     string `json:"kind"`
	NodeType string `json:"node_type,omitempty"`
}

func outlineSyntaxDiagnostics(diagnostics []syntaxhealth.Diagnostic) []SyntaxDiagnostic {
	out := make([]SyntaxDiagnostic, 0, len(diagnostics))
	for _, item := range diagnostics {
		out = append(out, SyntaxDiagnostic{Row: item.Row, Col: item.Col, Snippet: item.Snippet, Kind: item.Kind, NodeType: item.NodeType})
	}
	return out
}
