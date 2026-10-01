package guidance

import (
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
)

// maxTreeVerifyBytes caps the working-tree file a host citation check will read.
const maxTreeVerifyBytes = 4 << 20 // 4 MiB

// HostVerifyFindingAgainstTree reports whether a finding's verbatim excerpt is true
// on disk at the cited path. Survey-grade observation registers a file without a
// citable body; the host resolves that by reading the working tree and checking the
// excerpt with the same file_region verifier a full read would use.
func HostVerifyFindingAgainstTree(roots evidence.CitationRoots, citedPath string, line int, excerpt string) bool {
	excerpt = strings.TrimSpace(excerpt)
	citedPath = strings.TrimSpace(citedPath)
	if excerpt == "" || citedPath == "" {
		return false
	}
	if !evidence.ExcerptMeaningful(excerpt) {
		return false
	}
	abs, display, ok := evidence.ResolveCitationFile(roots, citedPath)
	if !ok {
		return false
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() || info.Size() > maxTreeVerifyBytes {
		return false
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return false
	}
	content := string(data)
	prefixed := evidence.FormatReadBodyForVerification(content)
	rec := evidence.Record{Kind: "read", Shape: evidence.ShapeFileRegion, Path: display, Body: []string{prefixed}}
	if total := evidence.CountTextLines(content); total > 0 {
		rec.LineRanges = []evidence.LineRange{{Start: 1, End: total}}
	}
	matched, _ := evidence.VerifyFileRegion(rec, evidence.Claim{Path: display, Line: line, Excerpt: excerpt})
	return matched
}
