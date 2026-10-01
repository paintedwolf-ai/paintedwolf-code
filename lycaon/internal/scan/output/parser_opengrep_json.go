package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/pkg/api"
)

type opengrepJSONParser struct{}

func newOpengrepJSONParser() OutputParser { return opengrepJSONParser{} }

func (opengrepJSONParser) ID() string { return OutputParserOpengrepJSON }

func (opengrepJSONParser) Parse(raw []byte) (*Result, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("opengrep_json: empty output")
	}
	result, err := ParseOpengrepJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("opengrep_json: parse %d bytes: %w", len(raw), err)
	}
	return result, nil
}

// ParseOpengrepJSON streams a report without retaining its envelope.
func ParseOpengrepJSON(r io.Reader) (*Result, error) {
	if r == nil {
		return nil, fmt.Errorf("opengrep_json: empty output")
	}
	decoder := json.NewDecoder(r)
	opening, err := decoder.Token()
	if err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("opengrep_json: empty output")
		}
		return nil, opengrepDecodeError(decoder, err)
	}
	if opening != json.Delim('{') {
		return nil, fmt.Errorf("opengrep_json: top-level value must be an object")
	}
	result := &Result{
		Categories: []api.ScanCategory{api.ScanCategorySAST, api.ScanCategorySecurity},
		Findings:   []api.SecurityFinding{},
	}
	var warnings []api.ScanWarning
	var fatals []opengrepJSONErr
	seen := make(map[string]bool)
	for decoder.More() {
		name, err := decoder.Token()
		if err != nil {
			return nil, opengrepDecodeError(decoder, err)
		}
		key, ok := name.(string)
		if !ok {
			return nil, fmt.Errorf("opengrep_json: invalid object key at byte %d", decoder.InputOffset())
		}
		if seen[key] {
			return nil, fmt.Errorf("opengrep_json: duplicate report field %q", key)
		}
		seen[key] = true
		switch key {
		case "paths":
			var paths struct {
				Scanned []string `json:"scanned"`
			}
			if err := decoder.Decode(&paths); err != nil {
				return nil, opengrepDecodeError(decoder, err)
			}
			result.ScannedPaths = paths.Scanned
		case "results":
			if err := decodeOpengrepResults(decoder, result); err != nil {
				return nil, err
			}
		case "errors":
			if err := decodeOpengrepErrors(decoder, &warnings, &fatals); err != nil {
				return nil, err
			}
		default:
			if err := skipJSONValue(decoder); err != nil {
				return nil, opengrepDecodeError(decoder, err)
			}
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return nil, opengrepDecodeError(decoder, err)
	}
	if closing != json.Delim('}') {
		return nil, fmt.Errorf("opengrep_json: invalid object close at byte %d", decoder.InputOffset())
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		return nil, err
	}
	if !seen["results"] {
		return nil, fmt.Errorf("opengrep_json: report has no results field")
	}
	if len(fatals) > 0 {
		return nil, fmt.Errorf("opengrep_json: %s", formatOpengrepFatalSummary(fatals))
	}
	result.Warnings = warnings
	return result, nil
}

func decodeOpengrepResults(decoder *json.Decoder, result *Result) error {
	opening, err := decoder.Token()
	if err != nil {
		return opengrepDecodeError(decoder, err)
	}
	if opening != json.Delim('[') {
		return fmt.Errorf("opengrep_json: results must be an array at byte %d", decoder.InputOffset())
	}
	for decoder.More() {
		var item opengrepResult
		if err := decoder.Decode(&item); err != nil {
			return opengrepDecodeError(decoder, err)
		}
		finding := findingFromOpengrep(item, result.Categories)
		finding.Dataflow, err = item.Extra.Dataflow.evidence()
		if err != nil {
			return fmt.Errorf("opengrep dataflow: %w", err)
		}
		if err := validateOpengrepFindingShape(finding); err != nil {
			return err
		}
		result.Findings = append(result.Findings, finding)
		result.FindingsCount++
	}
	if _, err := decoder.Token(); err != nil {
		return opengrepDecodeError(decoder, err)
	}
	return nil
}

func findingFromOpengrep(item opengrepResult, categories []api.ScanCategory) api.SecurityFinding {
	ruleID := canonicalOpengrepRuleID(item.CheckID)
	msg := item.Extra.Message
	if msg == "" {
		msg = item.CheckID
	}
	return scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
		DriverID: "opengrep",
		ToolName: "OpenGrep",
		RuleID:   ruleID,
		Level:    scanfindings.NormalizeVendorSeverity(item.Extra.Severity),
		Message:  msg,
		Kind:     api.FindingKindSAST,
		Locations: []api.SecurityFindingLocation{{
			URI:         item.Path,
			StartLine:   item.Start.Line,
			StartColumn: item.Start.Col,
			EndLine:     item.End.Line,
			EndColumn:   item.End.Col,
		}},
		Categories: categories,
	})
}

func decodeOpengrepErrors(decoder *json.Decoder, warnings *[]api.ScanWarning, fatals *[]opengrepJSONErr) error {
	opening, err := decoder.Token()
	if err != nil {
		return opengrepDecodeError(decoder, err)
	}
	if opening != json.Delim('[') {
		return fmt.Errorf("opengrep_json: errors must be an array at byte %d", decoder.InputOffset())
	}
	for decoder.More() {
		var item opengrepJSONErr
		if err := decoder.Decode(&item); err != nil {
			return opengrepDecodeError(decoder, err)
		}
		if warning, ok := opengrepErrorAsWarning(item); ok {
			*warnings = append(*warnings, warning)
		} else {
			*fatals = append(*fatals, item)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return opengrepDecodeError(decoder, err)
	}
	return nil
}

func skipJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' && delim != '[' {
		return nil
	}
	depth := 1
	for depth > 0 {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if nested, ok := token.(json.Delim); ok {
			switch nested {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}

func rejectTrailingJSON(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return opengrepDecodeError(decoder, err)
	}
	return fmt.Errorf("opengrep_json: trailing value at byte %d", decoder.InputOffset())
}

func opengrepDecodeError(decoder *json.Decoder, err error) error {
	return fmt.Errorf("opengrep_json: invalid JSON near byte %d: %w", decoder.InputOffset(), err)
}

func canonicalOpengrepRuleID(checkID string) string {
	id := strings.TrimSpace(checkID)
	if id == "" {
		return ""
	}
	if !strings.HasPrefix(id, "opengrep:") {
		id = "opengrep:" + id
	}
	body := strings.TrimPrefix(id, "opengrep:")
	for _, marker := range []string{".rules.vendor.", ".rules.lycaon."} {
		if idx := strings.Index(body, marker); idx >= 0 {
			return "opengrep:" + body[idx+len(marker):]
		}
	}
	return id
}

type opengrepJSONErr struct {
	Message     string           `json:"message"`
	Type        opengrepErrorTag `json:"type"`
	Level       string           `json:"level"`
	RuleID      string           `json:"rule_id"`
	Path        string           `json:"path"`
	Construct   string           `json:"-"`
	StartLine   int              `json:"-"`
	StartColumn int              `json:"-"`
}

type opengrepErrorTag string

func (t *opengrepErrorTag) UnmarshalJSON(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*t = ""
		return nil
	}
	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		*t = opengrepErrorTag(s)
	case '[':
		var tagged []json.RawMessage
		if err := json.Unmarshal(trimmed, &tagged); err != nil {
			return err
		}
		if len(tagged) == 0 {
			*t = ""
			return nil
		}
		var s string
		if err := json.Unmarshal(tagged[0], &s); err != nil {
			return err
		}
		*t = opengrepErrorTag(s)
	default:
		return fmt.Errorf("opengrep error type: unexpected JSON %s", trimmed)
	}
	return nil
}

func opengrepErrorAsWarning(e opengrepJSONErr) (api.ScanWarning, bool) {
	typ := strings.TrimSpace(string(e.Type))
	switch typ {
	case "PartialSemantics":
		return api.ScanWarning{
			Kind:        api.ScanWarningFilePartialSemantics,
			File:        strings.TrimSpace(e.Path),
			Message:     strings.TrimSpace(e.Message),
			Construct:   e.Construct,
			StartLine:   e.StartLine,
			StartColumn: e.StartColumn,
		}, true
	case "Rule parse error":
		ruleID := canonicalOpengrepRuleID(e.RuleID)
		msg := opengrepRuleParseDetail(e.Message)
		return api.ScanWarning{
			Kind:    api.ScanWarningRuleParseError,
			RuleID:  ruleID,
			Message: msg,
		}, true
	case "PartialParsing":
		return api.ScanWarning{
			Kind:        api.ScanWarningFilePartialParse,
			File:        strings.TrimSpace(e.Path),
			Message:     strings.TrimSpace(e.Message),
			StartLine:   e.StartLine,
			StartColumn: e.StartColumn,
		}, true
	case "Lexical error", "Syntax error", "Other syntax error", "AST builder error",
		"Internal matching error", "Fatal error", "Too many matches", "Timeout", "Out of memory", "Stack overflow":
		// Only a structured target path localizes an engine failure. Global and
		// unknown failures invalidate the run, regardless of unrelated findings.
		if strings.TrimSpace(e.Path) != "" {
			return api.ScanWarning{
				Kind:    api.ScanWarningTargetUnscanned,
				File:    strings.TrimSpace(e.Path),
				Message: typ + ": " + strings.TrimSpace(e.Message),
			}, true
		}
	}
	return api.ScanWarning{}, false
}

func opengrepRuleParseDetail(message string) string {
	msg := strings.TrimSpace(message)
	const prefix = "Rule parse error in rule "
	if !strings.HasPrefix(msg, prefix) {
		return msg
	}
	rest := strings.TrimPrefix(msg, prefix)
	if idx := strings.Index(rest, ": `"); idx >= 0 {
		rest = rest[idx+3:]
		if end := strings.LastIndex(rest, "`"); end >= 0 {
			rest = rest[:end]
		}
		return "invalid rule pattern: " + rest
	}
	return msg
}

func formatOpengrepFatalSummary(errs []opengrepJSONErr) string {
	var b strings.Builder
	for i, e := range errs {
		msg := strings.TrimSpace(e.Message)
		if msg == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("; ")
		}
		b.WriteString(msg)
		if i >= 2 {
			break
		}
	}
	if b.Len() == 0 {
		return "scan failed without a usable diagnostic (see opengrep stderr)"
	}
	return b.String()
}

type opengrepResult struct {
	CheckID string           `json:"check_id"`
	Path    string           `json:"path"`
	Start   opengrepPosition `json:"start"`
	End     opengrepPosition `json:"end"`
	Extra   opengrepExtra    `json:"extra"`
}

type opengrepPosition struct {
	Line int `json:"line"`
	Col  int `json:"col"`
}

type opengrepExtra struct {
	Dataflow *opengrepDataflow `json:"dataflow_trace"`
	Message  string            `json:"message"`
	Severity string            `json:"severity"`
}
