package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/jsonfence"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/pkg/api"
)

type compactionSummaryFormatKey struct{}

func WithSummaryFormat(ctx context.Context) context.Context {
	return context.WithValue(ctx, compactionSummaryFormatKey{}, true)
}

func SummaryFormatRequested(ctx context.Context) bool {
	v, _ := ctx.Value(compactionSummaryFormatKey{}).(bool)
	return v
}

type compactionSummary struct {
	SourceContext *api.SourceContext `json:"-"`
	Facts         []string           `json:"facts"`
	Completed     []string           `json:"completed"`
	Pending       []string           `json:"pending"`
	Reacquire     []string           `json:"reacquire"`
	Constraints   []string           `json:"constraints"`
}

// parseCompactionSummary decodes one complete continuation record.
func parseCompactionSummary(raw string) (compactionSummary, error) {
	if out, ok := jsonfence.ParseStrict(raw, decodeCompactionSummary); ok {
		return out, nil
	}
	// Direct decoding preserves field-specific errors.
	_, err := strictDecodeCompactionSummary(strings.TrimSpace(raw))
	if err == nil {
		err = fmt.Errorf("content is not a single JSON object")
	}
	return compactionSummary{}, fmt.Errorf("parse compaction summary: %w", err)
}

func decodeCompactionSummary(candidate string) (compactionSummary, bool) {
	out, err := strictDecodeCompactionSummary(candidate)
	if err != nil {
		return compactionSummary{}, false
	}
	return out, true
}

func strictDecodeCompactionSummary(trimmed string) (compactionSummary, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &fields); err != nil {
		return compactionSummary{}, err
	}
	for _, key := range []string{"facts", "completed", "pending", "reacquire", "constraints"} {
		if _, ok := fields[key]; !ok {
			return compactionSummary{}, fmt.Errorf("missing %s", key)
		}
	}
	var out compactionSummary
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return compactionSummary{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return compactionSummary{}, err
	}
	if out.Facts == nil || out.Completed == nil || out.Pending == nil || out.Reacquire == nil || out.Constraints == nil {
		return compactionSummary{}, fmt.Errorf("list fields must be arrays")
	}
	out.Facts = boundedSummaryLines(out.Facts, 8)
	out.Completed = boundedSummaryLines(out.Completed, 5)
	out.Pending = boundedSummaryLines(out.Pending, 5)
	out.Reacquire = boundedSummaryLines(out.Reacquire, 8)
	out.Constraints = boundedSummaryLines(out.Constraints, 5)
	if len(out.Facts) == 0 {
		return compactionSummary{}, fmt.Errorf("facts must contain at least one item")
	}
	return out, nil
}

func boundedSummaryLines(lines []string, maxItems int) []string {
	out := cleanSummaryLines(lines)
	if len(out) > maxItems {
		out = out[:maxItems]
	}
	for i := range out {
		out[i] = runeclamp.Fit(out[i], 160)
	}
	return out
}

func cleanSummaryLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// CompactedSource identifies a summarized or reduced row for recall.
type CompactedSource struct {
	Handles   []string
	Tool      string
	MessageID string
}

func (s CompactedSource) templateVars() map[string]any {
	return map[string]any{
		"handles":    append([]string(nil), s.Handles...),
		"tool":       s.Tool,
		"message_id": s.MessageID,
	}
}

// renderContinuationRecord combines summary data with host-authored recall instructions.
func renderContinuationRecord(ctx context.Context, summary compactionSummary, sources []CompactedSource, recallAvailable bool) (string, error) {
	rows := make([]map[string]any, 0, len(sources))
	for _, s := range sources {
		rows = append(rows, s.templateVars())
	}
	out, err := guidance.RenderGuidance(ctx, guidance.CompactionContinuationRecordRef, map[string]any{
		"facts":             summary.Facts,
		"completed":         summary.Completed,
		"pending":           summary.Pending,
		"reacquire":         summary.Reacquire,
		"constraints":       summary.Constraints,
		"compacted_sources": rows,
		"recall_available":  recallAvailable,
	})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("continuation record %q rendered empty", guidance.CompactionContinuationRecordRef)
	}
	return out, nil
}

func mockCompactionSummary(text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		text = "Continue coordinator work from compacted context."
	}
	raw, err := json.Marshal(compactionSummary{
		Facts:       []string{text},
		Completed:   []string{},
		Pending:     []string{},
		Reacquire:   []string{},
		Constraints: []string{},
	})
	if err != nil {
		return "", fmt.Errorf("marshal mock compaction summary: %w", err)
	}
	return string(raw), nil
}

var (
	// Bounds keep small local models from writing paragraph-sized items that
	// exhaust the context before the closing brace. Only facts has a floor.
	compactionSummarySchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["facts", "completed", "pending", "reacquire", "constraints"],
  "properties": {
    "facts": { "type": "array", "minItems": 1, "maxItems": 8, "items": { "type": "string", "minLength": 1, "maxLength": 160 } },
    "completed": { "type": "array", "maxItems": 5, "items": { "type": "string", "minLength": 1, "maxLength": 160 } },
    "pending": { "type": "array", "maxItems": 5, "items": { "type": "string", "minLength": 1, "maxLength": 160 } },
    "reacquire": { "type": "array", "maxItems": 8, "items": { "type": "string", "minLength": 1, "maxLength": 160 } },
    "constraints": { "type": "array", "maxItems": 5, "items": { "type": "string", "minLength": 1, "maxLength": 160 } }
  }
}`)
)

// CompactionSummaryResponseFormat constrains durable continuation synthesis.
func CompactionSummaryResponseFormat() *modelcall.ResponseFormat {
	return modelcall.JSONSchemaFormat("compaction_summary", compactionSummarySchema)
}
