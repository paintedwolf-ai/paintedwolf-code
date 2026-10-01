// Package safecmd applies the shared boundary for envelope-only tools.
package safecmd

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
)

// Caps declares the shared resource bounds the envelope enforces.
// Zero fields are unset (not enforced by the corresponding helper).
type Caps struct {
	InputBytes  int64
	Timeout     time.Duration
	ResultCount int
}

// JQCaps returns jq envelope limits.
func JQCaps() Caps {
	return Caps{
		InputBytes:  JQMaxInputBytes,
		Timeout:     JQTimeout,
		ResultCount: JQMaxResults,
	}
}

// StatCaps returns stat envelope limits.
func StatCaps() Caps {
	return Caps{ResultCount: StatMaxPaths}
}

// WCCaps returns wc envelope limits.
func WCCaps() Caps {
	return Caps{ResultCount: WCMaxFiles}
}

// MCPCaps returns MCP envelope limits.
func MCPCaps() Caps {
	return Caps{
		InputBytes: MCPMaxOutputBytes,
		Timeout:    MCPTimeout,
	}
}

// Tool-specific envelope limits.
const (
	JQMaxInputBytes     = 20 << 20
	JQTimeout           = 5 * time.Second
	JQMaxResults        = 2000
	JQZoomBytes         = 64 << 10
	JQScanCap           = 20000
	GrepMaxMatches      = 2000
	GrepTimeout         = 60 * time.Second
	GrepMaxPatternLen   = 512
	GrepMaxContextLines = 5
	// FindListingDepth shapes a find without name_glob; a name search walks every depth.
	FindListingDepth    = 8
	FindMaxResults      = 2000
	ListDirMaxEntries   = 2000
	ListDirMaxDepth     = 8
	StatMaxPaths        = 50
	WCMaxFiles          = 50
	WCMaxRecursiveFiles = 500
	MCPTimeout          = 60 * time.Second
	MCPMaxOutputBytes   = 256 << 10
	MCPZoomBytes        = 64 << 10
)

// SurveyCatalogJoin bounds how long a survey tool waits for the source catalog
// before answering from its last complete generation.
const SurveyCatalogJoin = 2 * time.Second

// Confine derives process confinement for an MCP server.
func Confine(roots []string) (*confine.Confinement, bool) {
	return confine.DefaultConfinement(confine.Request{Roots: roots})
}

// ResolvePath rejects parent traversal before resolving a project read path.
func ResolvePath(ctx context.Context, b *sandbox.Boundary, tctx tools.ToolContext, modelPath string) (projectpaths.Resolved, error) {
	if err := RejectPathEscape(modelPath); err != nil {
		return projectpaths.Resolved{}, err
	}
	return projectpaths.ResolveRead(ctx, b, tctx, modelPath)
}

// RejectPathEscape guards discovery paths that bypass ResolvePath.
func RejectPathEscape(modelPath string) error {
	if sandbox.HasParentTraversal(modelPath) {
		return Reject("SURVEY_PATH_ESCAPE", map[string]any{
			"path": strings.TrimSpace(modelPath),
		})
	}
	return nil
}

// Reject constructs a structured tool rejection.
func Reject(code string, data map[string]any) error {
	if data == nil {
		data = map[string]any{}
	}
	return &tools.ToolReject{Code: code, Data: data}
}

// EnforceInputBytes returns a structured reject when size exceeds Caps.InputBytes.
// No-op when InputBytes is unset or size is within the cap.
func (c Caps) EnforceInputBytes(size int64, code string, data map[string]any) error {
	if c.InputBytes <= 0 || size <= c.InputBytes {
		return nil
	}
	if data == nil {
		data = map[string]any{}
	}
	if _, ok := data["bytes"]; !ok {
		data["bytes"] = size
	}
	if _, ok := data["max_bytes"]; !ok {
		data["max_bytes"] = c.InputBytes
	}
	return Reject(code, data)
}

// WithTimeout derives a child context canceled after Caps.Timeout.
// When Timeout is unset, returns ctx and a no-op cancel.
func (c Caps) WithTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.Timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.Timeout)
}

// ShapeInput describes bounded tool output.
type ShapeInput struct {
	Tool         string
	Path         string
	PathsTouched int
	Truncated    bool
	Banner       string // empty for scalar tools
	Selected     int    // PatchCoverage when Total > 0
	Total        int
	Value        any
}

// Shape encodes a result with coverage and survey metadata.
func Shape(in ShapeInput) (string, error) {
	raw, err := toolkit.MarshalResponse(in.Value, in.Banner)
	if err != nil {
		return "", err
	}
	if in.Total > 0 {
		raw, err = toolkit.PatchCoverage(raw, in.Selected, in.Total)
		if err != nil {
			return "", err
		}
	}
	return surveyreceipt.Attach(raw, surveyreceipt.New(in.Tool, in.Path, in.PathsTouched, len(raw), in.Truncated)), nil
}
