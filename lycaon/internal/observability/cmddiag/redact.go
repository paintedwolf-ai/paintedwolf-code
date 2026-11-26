package cmddiag

import (
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/runeclamp"
)

// RedactReference applies the existing observability capture redaction rules
// and clamps references to a bounded length.
func RedactReference(ref string) string {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return ""
	}
	redacted := observability.RedactCaptureText(trimmed)
	return runeclamp.ClampBytes(redacted, DefaultMaxFieldBytes)
}

func sanitizeFinding(f Finding) Finding {
	f.SchemaVersion = SchemaVersion
	if f.SessionID != "" {
		f.SessionID = observability.ShortSessionID(observability.RedactCaptureText(strings.TrimSpace(f.SessionID)))
	}
	f.ProgramID = RedactReference(f.ProgramID)
	f.ParentReceipt = RedactReference(f.ParentReceipt)
	f.ToolCallID = RedactReference(f.ToolCallID)
	f.Reason = RedactReference(f.Reason)
	f.ExecutionIDs = sanitizeIDList(f.ExecutionIDs)
	f.OperationIDs = sanitizeIDList(f.OperationIDs)
	f.SourceIDs = sanitizeIDList(f.SourceIDs)
	f.Expected = sanitizeMap(f.Expected)
	f.Observed = sanitizeMap(f.Observed)
	return f
}

func sanitizeIDList(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, min(len(ids), DefaultMaxIDsPerFinding))
	for _, raw := range ids {
		clean := RedactReference(raw)
		if clean == "" {
			continue
		}
		if _, dup := seen[clean]; dup {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	slices.Sort(out)
	if len(out) > DefaultMaxIDsPerFinding {
		return out[:DefaultMaxIDsPerFinding]
	}
	return out
}

func sanitizeMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		cleanKey := RedactReference(k)
		if observability.IsSecretFieldName(cleanKey) {
			out[cleanKey] = "[REDACTED]"
			continue
		}
		out[cleanKey] = sanitizeValue(v)
	}
	return out
}

func sanitizeValue(v any) any {
	switch x := v.(type) {
	case string:
		return RedactReference(x)
	case []string:
		return sanitizeIDList(x)
	case map[string]any:
		return sanitizeMap(x)
	case *int:
		if x == nil {
			return nil
		}
		return *x
	default:
		return v
	}
}
