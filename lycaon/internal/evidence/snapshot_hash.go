package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// BodyFingerprint hashes joined body lines for canonical snapshot keys.
func BodyFingerprint(lines []string) string {
	if len(lines) == 0 {
		return "0"
	}
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:8])
}

// CanonicalSnapshotHash fingerprints a candidate snapshot ledger per tool-altitude SSOT.
func CanonicalSnapshotHash(ledger Ledger) string {
	handles := HandlesSorted(ledger)
	if len(handles) == 0 {
		return "empty"
	}
	parts := make([]string, 0, len(handles))
	for _, handle := range handles {
		rec, ok := ledger.Handles[handle]
		if !ok || rec.IsGate() {
			continue
		}
		parts = append(parts, strings.Join([]string{
			handle,
			rec.Kind,
			rec.Path,
			rec.SupersededBy,
			formatLineRanges(rec.LineRanges),
			BodyFingerprint(rec.Body),
		}, "|"))
	}
	if len(parts) == 0 {
		return "empty"
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:16])
}

// CurationCacheKey is hash(canonicalSnapshot) + "|" + focus.
func CurationCacheKey(ledger Ledger, focus string) string {
	return CanonicalSnapshotHash(ledger) + "|" + strings.TrimSpace(focus)
}

func formatLineRanges(ranges []LineRange) string {
	if len(ranges) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ranges))
	for _, r := range ranges {
		if r.Start <= 0 || r.End <= 0 {
			continue
		}
		if r.Start == r.End {
			parts = append(parts, fmt.Sprintf("%d", r.Start))
			continue
		}
		parts = append(parts, fmt.Sprintf("%d-%d", r.Start, r.End))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}
