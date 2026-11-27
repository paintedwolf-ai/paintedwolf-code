package watchdiag

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/runeclamp"
)

// DigestReference hashes sensitive reference parts (such as workspace roots or
// file paths) into a bounded, opaque digest ("ref:<sha256[:12]>") so raw path
// strings never enter ordinary dev diagnostics.
func DigestReference(parts ...string) string {
	h := sha256.New()
	hasNonEmpty := false
	for i, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			hasNonEmpty = true
		}
		if i > 0 {
			_, _ = h.Write([]byte{0})
		}
		_, _ = h.Write([]byte(trimmed))
	}
	if !hasNonEmpty {
		return ""
	}
	sum := h.Sum(nil)
	return "ref:" + hex.EncodeToString(sum[:6])
}

// RedactReference applies observability capture redaction rules and clamps
// references to a bounded byte length. Path-like strings containing directory
// separators are hashed via DigestReference.
func RedactReference(ref string) string {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return ""
	}
	if strings.ContainsAny(trimmed, `/\`) {
		return DigestReference(trimmed)
	}
	redacted := observability.RedactCaptureText(trimmed)
	return runeclamp.ClampBytes(redacted, DefaultMaxFieldBytes)
}

func sanitizeSessionID(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	return observability.ShortSessionID(observability.RedactCaptureText(trimmed))
}

func sanitizeClockDomain(domain ClockDomain) ClockDomain {
	switch ClockDomain(strings.TrimSpace(string(domain))) {
	case ClockDomainSystem:
		return ClockDomainSystem
	case ClockDomainManual:
		return ClockDomainManual
	default:
		return ""
	}
}

func sanitizeOwningSource(src OwningSource) OwningSource {
	return OwningSource{
		Component:   RedactReference(src.Component),
		Phase:       RedactReference(src.Phase),
		ClockDomain: sanitizeClockDomain(src.ClockDomain),
		EvidenceRef: RedactReference(src.EvidenceRef),
	}
}

func sanitizeSourceRef(ref SourceRef) SourceRef {
	return SourceRef{
		SessionID:    sanitizeSessionID(ref.SessionID),
		WorkerJobID:  RedactReference(ref.WorkerJobID),
		WatchID:      RedactReference(ref.WatchID),
		Handle:       RedactReference(ref.Handle),
		OccurrenceID: RedactReference(ref.OccurrenceID),
		ClaimID:      RedactReference(ref.ClaimID),
		TurnID:       RedactReference(ref.TurnID),
		StopEpoch:    ref.StopEpoch,
		EvidenceRef:  RedactReference(ref.EvidenceRef),
		OwningSource: sanitizeOwningSource(ref.OwningSource),
	}
}

func sanitizeSources(refs []SourceRef) []SourceRef {
	if len(refs) == 0 {
		return nil
	}
	limit := min(len(refs), DefaultMaxIDsPerFinding)
	out := make([]SourceRef, 0, limit)
	for _, ref := range refs[:limit] {
		out = append(out, sanitizeSourceRef(ref))
	}
	return out
}

func sanitizeFinding(f Finding) Finding {
	f.SchemaVersion = SchemaVersion
	f.SessionID = sanitizeSessionID(f.SessionID)
	f.WorkerJobID = RedactReference(f.WorkerJobID)
	f.WatchID = RedactReference(f.WatchID)
	f.Handle = RedactReference(f.Handle)
	f.OccurrenceID = RedactReference(f.OccurrenceID)
	f.ClaimID = RedactReference(f.ClaimID)
	f.TurnID = RedactReference(f.TurnID)
	f.EvidenceRef = RedactReference(f.EvidenceRef)
	f.OwningSource = sanitizeOwningSource(f.OwningSource)
	f.Sources = sanitizeSources(f.Sources)
	f.Reason = RedactReference(f.Reason)
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
	case ClockDomain:
		return string(sanitizeClockDomain(x))
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
