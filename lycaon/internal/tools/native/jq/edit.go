package jq

import (
	"context"
	"errors"

	"github.com/itchyny/gojq"

	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

// EditToolName is the id the structured-document write tool registers under.
const EditToolName = "jq_edit"

// EditRequest is one jq transformation of a structured document's text.
type EditRequest struct {
	// Path names the source in rejections.
	Path   string
	Format string
	Query  string
	Text   string
	Vars   map[string]any
}

// EditResult is the re-encoded document.
type EditResult struct {
	Format    string
	Text      string
	Documents int
}

// editDocument is a parsed source that re-encodes query results in its own
// format, preserving what the source states beyond the decoded values.
type editDocument interface {
	inputs() []any
	encode(results []editValue) (string, error)
}

// editValue is one query result and the source document that produced it.
type editValue struct {
	value any
	input int
}

// errLossy reports a source construct the re-encoded document cannot carry.
type errLossy struct {
	kind string
	line int
}

func (e *errLossy) Error() string {
	return "document holds a " + e.kind + " that the rewrite cannot preserve"
}

// errEncode reports a result the source format cannot represent.
type errEncode struct{ detail string }

func (e *errEncode) Error() string { return e.detail }

// Edit runs query over the document and re-encodes the results in the
// source's format. It never writes; the caller lands the text.
func Edit(ctx context.Context, req EditRequest) (EditResult, error) {
	format, err := detectFormat(req.Path, req.Format)
	if err != nil {
		return EditResult{}, err
	}
	doc, parsedAs, err := parseEditDocument(req.Text, format)
	if err != nil {
		return EditResult{}, editParseReject(req.Path, format, parsedAs, err)
	}
	code, varValues, err := compileQueryWithVars(req.Query, req.Vars)
	if err != nil {
		return EditResult{}, safecmd.Reject("JQ_QUERY_INVALID", map[string]any{
			"query": req.Query, "detail": err.Error(),
		})
	}
	results, err := runEditQuery(ctx, code, doc.inputs(), req, varValues...)
	if err != nil {
		return EditResult{}, err
	}
	if len(results) == 0 {
		return EditResult{}, safecmd.Reject("JQ_EDIT_EMPTY", map[string]any{"path": req.Path, "query": req.Query})
	}
	if len(doc.inputs()) == 1 && len(results) > 1 {
		return EditResult{}, safecmd.Reject("JQ_EDIT_RESULT_COUNT", map[string]any{
			"path": req.Path, "count": len(results),
		})
	}
	text, err := doc.encode(results)
	if err != nil {
		return EditResult{}, editEncodeReject(req.Path, parsedAs, err)
	}
	return EditResult{Format: parsedAs, Text: text, Documents: len(results)}, nil
}

func parseEditDocument(text, format string) (editDocument, string, error) {
	switch format {
	case "json":
		doc, err := parseJSONEdit(text)
		return doc, "json", err
	case "yaml":
		doc, err := parseYAMLEdit(text)
		return doc, "yaml", err
	case "toml":
		doc, err := parseTOMLEdit(text)
		return doc, "toml", err
	}
	if doc, err := parseJSONEdit(text); err == nil {
		return doc, "json", nil
	}
	if doc, err := parseYAMLEdit(text); err == nil {
		return doc, "yaml", nil
	} else if lossyOrDepth(err) {
		return nil, "yaml", err
	}
	if doc, err := parseTOMLEdit(text); err == nil {
		return doc, "toml", nil
	} else if lossyOrDepth(err) {
		return nil, "toml", err
	}
	return nil, "", errors.New("could not parse document as JSON, YAML, or TOML")
}

func lossyOrDepth(err error) bool {
	var lossy *errLossy
	return errors.As(err, &lossy) || errors.Is(err, errDocumentDepthExceeded)
}

func editParseReject(path, format, parsedAs string, err error) error {
	if errors.Is(err, errDocumentDepthExceeded) {
		return safecmd.Reject("JQ_DOCUMENT_DEPTH", map[string]any{"path": path, "max_depth": maxDocumentDepth})
	}
	var lossy *errLossy
	if errors.As(err, &lossy) {
		return lossyReject(path, parsedAs, lossy)
	}
	if parsedAs != "" {
		format = parsedAs
	}
	return safecmd.Reject("JQ_PARSE", map[string]any{"path": path, "format": format, "detail": err.Error()})
}

func editEncodeReject(path, format string, err error) error {
	var lossy *errLossy
	if errors.As(err, &lossy) {
		return lossyReject(path, format, lossy)
	}
	return safecmd.Reject("JQ_EDIT_ENCODE", map[string]any{"path": path, "format": format, "detail": err.Error()})
}

func lossyReject(path, format string, lossy *errLossy) error {
	data := map[string]any{"path": path, "format": format, "kind": lossy.kind}
	if lossy.line > 0 {
		data["line"] = lossy.line
	}
	return safecmd.Reject("JQ_EDIT_LOSSY", data)
}

// runEditQuery keeps every result; a stream past the scan cap is refused
// rather than written short.
func runEditQuery(ctx context.Context, code *gojq.Code, inputs []any, req EditRequest, varValues ...any) ([]editValue, error) {
	caps := safecmd.JQCaps()
	runCtx, cancel := caps.WithTimeout(ctx)
	defer cancel()

	var results []editValue
	for i, input := range inputs {
		iter := code.RunWithContext(runCtx, input, varValues...)
		for {
			v, ok := iter.Next()
			if !ok {
				break
			}
			if verr, isErr := v.(error); isErr {
				if runCtx.Err() != nil {
					return nil, safecmd.Reject("JQ_TIMEOUT", map[string]any{
						"query": req.Query, "timeout_ms": caps.Timeout.Milliseconds(),
					})
				}
				var halt *gojq.HaltError
				if errors.As(verr, &halt) {
					break
				}
				return nil, safecmd.Reject("JQ_QUERY_ERROR", map[string]any{
					"query": req.Query, "detail": verr.Error(),
				})
			}
			if len(results) >= safecmd.JQScanCap {
				return nil, safecmd.Reject("JQ_EDIT_STREAM_CAP", map[string]any{
					"path": req.Path, "cap": safecmd.JQScanCap,
				})
			}
			results = append(results, editValue{value: v, input: i})
		}
	}
	return results, nil
}
