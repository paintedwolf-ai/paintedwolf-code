package jq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/itchyny/gojq"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/zstdcodec"
	"github.com/lycaon/lycaon/pkg/api"
)

// ToolName is the id the jq tool registers under.
const ToolName = "jq"

// Tool queries structured documents under the project boundary.
type Tool struct {
	Boundary *sandbox.Boundary
}

type diagnostics struct {
	Hint string `json:"hint"`
}

type response struct {
	Path   string `json:"path"`
	Format string `json:"format,omitempty"`
	Query  string `json:"query,omitempty"`

	// Literal mode.
	Values      []json.RawMessage `json:"values,omitempty"`
	Offset      int               `json:"offset,omitempty"`
	Count       int               `json:"count,omitempty"`
	ResultTotal int               `json:"result_total,omitempty"`
	Truncated   bool              `json:"truncated"`
	NextOffset  *int              `json:"next_offset,omitempty"`

	// Zoomed-out mode.
	Shape       *shapeNode   `json:"shape,omitempty"`
	Note        string       `json:"note,omitempty"`
	Selected    int          `json:"selected,omitempty"`
	Total       int          `json:"total,omitempty"`
	Diagnostics *diagnostics `json:"diagnostics,omitempty"`

	TruncationBanner string `json:"truncation_banner,omitempty"`
}

// Run executes a bounded query against a confined structured document.
func (t *Tool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	relPath := ""
	if raw, ok := args["path"].(string); ok {
		relPath = strings.TrimSpace(raw)
	}
	if relPath == "" {
		return "", safecmd.Reject("JQ_PATH_REQUIRED", map[string]any{})
	}
	formatArg, _ := args["format"].(string)

	query := "."
	if raw, ok := args["query"].(string); ok && strings.TrimSpace(raw) != "" {
		query = strings.TrimSpace(raw)
	}

	maxResults := safecmd.JQCaps().ResultCount
	offset := toolkit.ClampIntArg(args, "offset", 0, 0, 1_000_000)
	limit := 0
	if _, ok := args["limit"]; ok {
		limit = toolkit.ClampIntArg(args, "limit", maxResults, 1, maxResults)
	} else if _, ok := args["offset"]; ok {
		limit = maxResults
	}

	resolved, err := safecmd.ResolvePath(ctx, t.Boundary, tctx, relPath)
	if err != nil {
		return "", err
	}
	displayPath := filepath.ToSlash(resolved.DisplayPath)
	if tools.IsSensitivePath(displayPath) {
		return "", safecmd.Reject("JQ_PATH_DENIED", map[string]any{"path": displayPath})
	}
	data, err := readInput(resolved, displayPath, tctx.Host.MaxToolSpillBytes)
	if err != nil {
		return "", err
	}

	resolvedFormat, err := detectFormat(displayPath, formatArg)
	if err != nil {
		return "", err
	}
	inputs, parsedAs, err := decodeInputs(data, resolvedFormat)
	if err != nil {
		if errors.Is(err, errDocumentDepthExceeded) {
			return "", safecmd.Reject("JQ_DOCUMENT_DEPTH", map[string]any{
				"path": displayPath, "max_depth": maxDocumentDepth,
			})
		}
		return "", safecmd.Reject("JQ_PARSE", map[string]any{
			"path": displayPath, "format": resolvedFormat, "detail": err.Error(),
		})
	}

	code, err := compileQuery(query)
	if err != nil {
		return "", safecmd.Reject("JQ_QUERY_INVALID", map[string]any{
			"query": query, "detail": err.Error(),
		})
	}

	results, truncatedScan, err := runQuery(ctx, code, inputs, query)
	if err != nil {
		return "", err
	}

	tctx.RecordSourcePath(resolved.Abs, api.NavigationEntryKindFile)
	resp := response{Path: displayPath, Format: parsedAs, Query: query}
	if note := normalizedFormatNote(parsedAs); note != "" {
		resp.Note = note
	}
	totalBytes, big := resultsTooBig(results)
	if !hasExpressedScope(args) && (big || truncatedScan) {
		resp = buildZoomedOutResponse(ctx, resp, results, totalBytes)
		resp.Path = displayPath
		resp.Format = parsedAs
		resp.Query = query
		return encodeResponse(displayPath, resp)
	}

	vals, truncated, next := literalPage(results, offset, limit)
	resp.Values = vals
	resp.Count = len(vals)
	resp.ResultTotal = len(results)
	resp.Offset = offset
	resp.Truncated = truncated || truncatedScan
	if truncated {
		resp.NextOffset = &next
	}
	if banner := truncationBanner(resp, truncatedScan); banner != "" {
		resp.TruncationBanner = banner
	}
	return encodeResponse(displayPath, resp)
}

func readInput(resolved projectpaths.Resolved, displayPath string, maxSpillBytes int) ([]byte, error) {
	caps := safecmd.JQCaps()
	if resolved.ToolOutput {
		caps.InputBytes = int64(tooloutput.EffectiveMaxSpillFileBytes(maxSpillBytes))
	}
	f, err := fseffect.OpenRead(resolved.EffectLocation())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, safecmd.Reject("JQ_PATH_NOT_FOUND", map[string]any{"path": displayPath})
		}
		return nil, fmt.Errorf("jq stat failed: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("jq stat failed: %w", err)
	}
	if info.IsDir() {
		return nil, safecmd.Reject("READ_IS_DIRECTORY", map[string]any{"path": displayPath})
	}
	var data []byte
	if resolved.Compressed {
		data, err = zstdcodec.ReadBounded(f, caps.InputBytes)
		if errors.Is(err, zstdcodec.ErrDecodedLimit) {
			return nil, safecmd.Reject("JQ_INPUT_TOO_LARGE", map[string]any{"path": displayPath, "max_bytes": caps.InputBytes})
		}
		if err != nil {
			return nil, fmt.Errorf("jq decompress failed: %w", err)
		}
	} else {
		// Stat-first so the cap bounds the allocation, not just the accepted input.
		if err := caps.EnforceInputBytes(info.Size(), "JQ_INPUT_TOO_LARGE", map[string]any{
			"path": displayPath,
		}); err != nil {
			return nil, err
		}
		readLimit := caps.InputBytes
		data, err = io.ReadAll(io.LimitReader(f, readLimit+1))
		if err != nil {
			if os.IsNotExist(err) {
				return nil, safecmd.Reject("JQ_PATH_NOT_FOUND", map[string]any{"path": displayPath})
			}
			return nil, fmt.Errorf("jq read failed: %w", err)
		}
	}
	if err := caps.EnforceInputBytes(int64(len(data)), "JQ_INPUT_TOO_LARGE", map[string]any{"path": displayPath}); err != nil {
		return nil, err
	}
	doc, _, err := textfile.Open(data, textfile.LimitsForRaw(caps.InputBytes))
	if err != nil {
		return nil, safecmd.Reject("READ_BINARY_DENIED", map[string]any{"path": displayPath, "bytes": len(data), "reason": err.Error()})
	}
	data = []byte(doc.Text())
	return data, nil
}

// compileQuery disables environment and input extensions.
func compileQuery(query string) (*gojq.Code, error) {
	code, _, err := compileQueryWithVars(query, nil)
	return code, err
}

func compileQueryWithVars(query string, vars map[string]any) (*gojq.Code, []any, error) {
	q, err := gojq.Parse(query)
	if err != nil {
		return nil, nil, err
	}
	if len(vars) == 0 {
		code, err := gojq.Compile(q, gojq.WithEnvironLoader(func() []string { return nil }))
		return code, nil, err
	}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	varNames := make([]string, len(keys))
	varValues := make([]any, len(keys))
	for i, k := range keys {
		varNames[i] = "$" + strings.TrimPrefix(k, "$")
		varValues[i] = vars[k]
	}
	code, err := gojq.Compile(q,
		gojq.WithEnvironLoader(func() []string { return nil }),
		gojq.WithVariables(varNames),
	)
	return code, varValues, err
}

func runQuery(ctx context.Context, code *gojq.Code, inputs []any, query string) ([]any, bool, error) {
	caps := safecmd.JQCaps()
	runCtx, cancel := caps.WithTimeout(ctx)
	defer cancel()

	var results []any
	for _, input := range inputs {
		iter := code.RunWithContext(runCtx, input)
		for {
			v, ok := iter.Next()
			if !ok {
				break
			}
			if verr, isErr := v.(error); isErr {
				if runCtx.Err() != nil {
					return nil, false, safecmd.Reject("JQ_TIMEOUT", map[string]any{
						"query": query, "timeout_ms": caps.Timeout.Milliseconds(),
					})
				}
				var halt *gojq.HaltError
				if errors.As(verr, &halt) {
					break
				}
				return nil, false, safecmd.Reject("JQ_QUERY_ERROR", map[string]any{
					"query": query, "detail": verr.Error(),
				})
			}
			results = append(results, v)
			if len(results) >= safecmd.JQScanCap {
				return results, true, nil
			}
		}
	}
	return results, false, nil
}

func hasExpressedScope(args map[string]any) bool {
	if _, ok := args["offset"]; ok {
		return true
	}
	if _, ok := args["limit"]; ok {
		return true
	}
	return false
}

func resultsTooBig(results []any) (int, bool) {
	if len(results) > safecmd.JQMaxResults {
		return 0, true
	}
	total := 0
	for _, r := range results {
		b, err := surveyjson.Marshal(r)
		if err != nil {
			continue
		}
		total += len(b)
		if total > safecmd.JQZoomBytes {
			return total, true
		}
	}
	return total, false
}

func literalPage(results []any, offset, limit int) ([]json.RawMessage, bool, int) {
	if offset > len(results) {
		offset = len(results)
	}
	end := len(results)
	truncated := false
	if limit > 0 && offset+limit < end {
		end = offset + limit
		truncated = true
	}
	vals := make([]json.RawMessage, 0, end-offset)
	for _, r := range results[offset:end] {
		b, err := surveyjson.Marshal(r)
		if err != nil {
			b = []byte("null")
		}
		vals = append(vals, json.RawMessage(b))
	}
	return vals, truncated, end
}

func truncationBanner(resp response, scanTrunc bool) string {
	var parts []string
	if resp.Truncated && resp.NextOffset != nil {
		parts = append(parts, fmt.Sprintf("showing %d of %d values from offset %d; use offset=%d",
			resp.Count, resp.ResultTotal, resp.Offset, *resp.NextOffset))
	}
	if scanTrunc {
		parts = append(parts, fmt.Sprintf("result stream exceeded %d values — narrow the query", safecmd.JQScanCap))
	}
	if len(parts) == 0 {
		return ""
	}
	return toolkit.TruncationBanner(strings.Join(parts, "; "))
}

func encodeResponse(path string, resp response) (string, error) {
	return safecmd.Shape(safecmd.ShapeInput{
		Tool:         "jq",
		Path:         path,
		PathsTouched: 1,
		Truncated:    resp.Truncated,
		Banner:       resp.TruncationBanner,
		Selected:     resp.Selected,
		Total:        resp.Total,
		Value:        resp,
	})
}
