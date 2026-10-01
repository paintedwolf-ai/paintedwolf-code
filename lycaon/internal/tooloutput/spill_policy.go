package tooloutput

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/runeclamp"
)

const (
	// ToolResultTooLargeCode rejects oversized output without a truncation receipt.
	ToolResultTooLargeCode = "TOOL_RESULT_TOO_LARGE"
	// ToolOutputSpillCapExceededCode refuses incomplete whole-observation retention.
	ToolOutputSpillCapExceededCode = "TOOL_OUTPUT_SPILL_CAP_EXCEEDED"
	// ToolOutputSpillUnavailableCode refuses lossy delivery when recovery storage failed.
	ToolOutputSpillUnavailableCode = "TOOL_OUTPUT_SPILL_UNAVAILABLE"
)

// WireSpillOutcome is the host decision for inline preview vs on-disk spill.
type WireSpillOutcome struct {
	Preview       string
	SpillPath     string
	Truncated     bool
	SpillCapped   bool
	OriginalBytes int
	SpillBytes    int
	RejectCode    string
	RejectData    map[string]any
}

// HasTruncationReceipt reports whether tool JSON already admits partial output.
func HasTruncationReceipt(content string) bool {
	_, body, _, ok := hostmarker.SplitToolJSONBody(content)
	if !ok {
		return false
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		return false
	}
	if truncated, ok := obj["truncated"].(bool); ok && truncated {
		return true
	}
	if receipt, ok := obj["receipt"].(map[string]any); ok {
		if truncated, ok := receipt["truncated"].(bool); ok && truncated {
			return true
		}
	}
	banner, _ := obj["truncation_banner"].(string)
	return strings.Contains(banner, "TOOL_SURVEY_BYTE_CLAMPED")
}

// IsStructuredToolJSON reports whether content carries a parseable JSON object body.
func IsStructuredToolJSON(content string) bool {
	_, _, _, ok := hostmarker.SplitToolJSONBody(content)
	return ok
}

// WireSpillToolOutput persists overflow at a content address.
// JSON is indented for paging; opaque bytes stay exact.
func WireSpillToolOutput(hostDataDir string, content ScreenedOutput, maxBytes, maxSpillBytes int) WireSpillOutcome {
	body := content.String()
	out := WireSpillOutcome{Preview: body, OriginalBytes: len(body)}
	if maxBytes <= 0 || len(body) <= maxBytes {
		return out
	}
	out.Truncated = true
	out.Preview = truncateInline(body, maxBytes)
	out = writeSpill(out, hostDataDir, body, LineAddressableToolJSON(body), maxSpillBytes, false)
	if out.RejectCode == "" && out.SpillPath == "" {
		out.RejectCode = ToolOutputSpillUnavailableCode
		out.RejectData = map[string]any{"bytes": len(body)}
		out.Preview = ""
	}
	if out.SpillPath != "" && IsStructuredToolJSON(body) {
		residue := InjectWireSpillPath(body, out.SpillPath)
		if len(residue) <= maxBytes {
			out.Preview = residue
		} else if fitted, ok := FitWireJSON(residue, maxBytes); ok {
			out.Preview = fitted
		} else {
			out.RejectCode = ToolResultTooLargeCode
			out.RejectData = map[string]any{"bytes": len(body), "cap": maxBytes, "reason": "structured_residue_overflow"}
			out.Preview = ""
		}
	}
	return out
}

// SpillWholeToolOutput persists a tool wire body in full, line-addressable.
func SpillWholeToolOutput(hostDataDir string, content ScreenedOutput, maxSpillBytes int) WireSpillOutcome {
	body := content.String()
	return writeSpill(WireSpillOutcome{Truncated: true, OriginalBytes: len(body)}, hostDataDir, body, LineAddressableToolJSON(body), maxSpillBytes, true)
}

// SpillWholeRaw persists a screened text body in full, byte-exact.
func SpillWholeRaw(hostDataDir string, content ScreenedOutput, maxSpillBytes int) WireSpillOutcome {
	body := content.String()
	return writeSpill(WireSpillOutcome{Truncated: true, OriginalBytes: len(body)}, hostDataDir, body, body, maxSpillBytes, true)
}

// LineAddressableToolJSON indents the JSON payload while preserving its envelope.
func LineAddressableToolJSON(content string) string {
	prefix, jsonBody, suffix, ok := hostmarker.SplitToolJSONBody(content)
	if !ok {
		return content
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, []byte(jsonBody), "", "  "); err != nil {
		return content
	}
	return prefix + indented.String() + suffix
}

// writeSpill retains renderings without aliasing partial and complete bodies.
func writeSpill(out WireSpillOutcome, hostDataDir, body, payload string, maxSpillBytes int, requireWhole bool) WireSpillOutcome {
	hostDataDir = strings.TrimSpace(hostDataDir)
	if hostDataDir == "" {
		return out
	}
	// The cap is judged on the body; a rendering that would not fit falls back
	// to the body itself.
	if len(payload) > EffectiveMaxSpillFileBytes(maxSpillBytes) {
		payload = body
	}
	spillPayload, capped, reject := spillPayloadForWrite(payload, maxSpillBytes, requireWhole)
	if reject {
		out.RejectCode = ToolOutputSpillCapExceededCode
		out.RejectData = map[string]any{
			"bytes": len(body),
			"cap":   EffectiveMaxSpillFileBytes(maxSpillBytes),
		}
		return out
	}
	out.SpillCapped = capped
	out.SpillBytes = len(spillPayload)
	relPath := ToolOutputSpillRelPath(body)
	if capped {
		// A partial rendering gets its own address, so it cannot replace a retained full body.
		relPath = ToolOutputSpillRelPath(spillPayload)
	}

	// Spill writes are streamed, atomic, and mode 0600.
	store := blobstore.Store{Root: hostDataDir}
	bound := bytebound.Materialization(EffectiveMaxSpillFileBytes(maxSpillBytes))
	if _, err := store.PutAt(relPath, strings.NewReader(spillPayload), bound); err != nil {
		return out
	}
	out.SpillPath = relPath
	return out
}

// DiskPath joins a host-data-relative wire spill path onto hostDataDir.
func DiskPath(hostDataDir, wireRel string) string {
	return filepath.Join(strings.TrimSpace(hostDataDir), filepath.FromSlash(strings.TrimSpace(wireRel)))
}

// RemoveSpillBefore reclaims a settled tool-output body.
func RemoveSpillBefore(hostDataDir, wireRel string, cutoff time.Time) (bool, error) {
	wireRel = filepath.ToSlash(strings.TrimSpace(wireRel))
	if !strings.HasPrefix(wireRel, ToolOutputSpillDir+"/") || !IsAgentWireSpillRel(wireRel) {
		return false, nil
	}
	return (blobstore.Store{Root: strings.TrimSpace(hostDataDir)}).RemoveAtBefore(wireRel, cutoff)
}

func spillPayloadForWrite(content string, maxSpillBytes int, requireWhole bool) (payload string, capped, reject bool) {
	cap := EffectiveMaxSpillFileBytes(maxSpillBytes)
	if len(content) <= cap {
		return content, false, false
	}
	if requireWhole || IsStructuredToolJSON(content) {
		return "", false, true
	}
	return CapSpillBytes(content, maxSpillBytes), true, false
}

func truncateInline(content string, maxBytes int) string {
	const suffix = runeclamp.TruncatedSuffix
	if maxBytes <= len(suffix) {
		return runeclamp.CutBytes(content, maxBytes)
	}
	return runeclamp.CutBytes(content, maxBytes-len(suffix)) + suffix
}
