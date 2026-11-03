package checkpoint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// ConflictKind classifies a promotion conflict requiring manual or policy resolution.
type ConflictKind string

const (
	ConflictMissingExactBase   ConflictKind = "missing_exact_base"
	ConflictInvalidBaseDigest  ConflictKind = "invalid_base_digest"
	ConflictAmbiguousBase      ConflictKind = "ambiguous_base"
	ConflictContentOverlap     ConflictKind = "content_overlap"
	ConflictModifyDelete       ConflictKind = "modify_delete"
	ConflictAddAddDifference   ConflictKind = "add_add_difference"
	ConflictDirectoryReplaced  ConflictKind = "directory_replacement_conflict"
	ConflictTypeMismatch       ConflictKind = "type_mismatch"
)

// ResolutionChoice identifies a supported manual resolution strategy for a PromotionConflict.
type ResolutionChoice string

const (
	ResolutionKeepTarget  ResolutionChoice = "keep_target"
	ResolutionApplyWorker ResolutionChoice = "apply_worker"
	ResolutionManualBytes ResolutionChoice = "manual_bytes"
)

// ConflictResolution supplies an explicit operator/reviewer decision for a conflicted path.
type ConflictResolution struct {
	Path        layeredfs.QualifiedPath `json:"path"`
	Choice      ResolutionChoice        `json:"choice"`
	ManualBytes []byte                  `json:"manual_bytes,omitempty"`
}

// PromotionConflict records recoverable conflict evidence and supported resolutions.
type PromotionConflict struct {
	Path                 layeredfs.QualifiedPath   `json:"path"`
	Kind                 ConflictKind              `json:"kind"`
	Message              string                    `json:"message"`
	Base                 layeredfs.BaseCapture     `json:"base"`
	CandidateBases       []layeredfs.BaseCapture   `json:"candidate_bases,omitempty"`
	TargetObservation    TargetObservation         `json:"target_observation"`
	WorkerFinal          layeredfs.ChangeFinalAttr `json:"worker_final"`
	AmbiguousOutputs     [][]byte                  `json:"ambiguous_outputs,omitempty"`
	SupportedResolutions []ResolutionChoice        `json:"supported_resolutions"`
}

func defaultResolutions() []ResolutionChoice {
	return []ResolutionChoice{
		ResolutionKeepTarget,
		ResolutionApplyWorker,
		ResolutionManualBytes,
	}
}

// ThreeWayMergeLines performs a deterministic line-based 3-way merge of base, target, and worker.
// It returns (mergedBytes, true) when changes do not overlap, or (nil, false) on conflict.
func ThreeWayMergeLines(base, target, worker []byte) ([]byte, bool) {
	if bytes.Equal(base, target) {
		return append([]byte(nil), worker...), true
	}
	if bytes.Equal(base, worker) || bytes.Equal(target, worker) {
		return append([]byte(nil), target...), true
	}
	bLines := splitLinesKeepTerm(string(base))
	tLines := splitLinesKeepTerm(string(target))
	wLines := splitLinesKeepTerm(string(worker))

	tEdits, tOK := diffLineAnchored(bLines, tLines)
	wEdits, wOK := diffLineAnchored(bLines, wLines)
	if !tOK || !wOK {
		return nil, false
	}
	return combineLineEdits(bLines, tEdits, wEdits)
}

type lineEdit struct {
	startIdx int
	endIdx   int
	repl     []string
}

func diffLineAnchored(base, mod []string) ([]lineEdit, bool) {
	if len(base) == 0 {
		if len(mod) == 0 {
			return nil, true
		}
		return []lineEdit{{startIdx: 0, endIdx: 0, repl: mod}}, true
	}
	// Map unique anchor lines in base and mod for clean non-overlapping hunk extraction.
	bAnchors, mAnchors := findSharedUniqueAnchors(base, mod)
	var edits []lineEdit
	bPos, mPos := 0, 0
	for i := 0; i < len(bAnchors); i++ {
		bIdx, mIdx := bAnchors[i], mAnchors[i]
		if bIdx > bPos || mIdx > mPos {
			if !slicesEqual(base[bPos:bIdx], mod[mPos:mIdx]) {
				edits = append(edits, lineEdit{
					startIdx: bPos,
					endIdx:   bIdx,
					repl:     append([]string(nil), mod[mPos:mIdx]...),
				})
			}
		}
		bPos = bIdx + 1
		mPos = mIdx + 1
	}
	if bPos < len(base) || mPos < len(mod) {
		if !slicesEqual(base[bPos:], mod[mPos:]) {
			edits = append(edits, lineEdit{
				startIdx: bPos,
				endIdx:   len(base),
				repl:     append([]string(nil), mod[mPos:]...),
			})
		}
	}
	return edits, true
}

func findSharedUniqueAnchors(base, mod []string) ([]int, []int) {
	bCount := make(map[string]int, len(base))
	mCount := make(map[string]int, len(mod))
	mIndex := make(map[string]int, len(mod))
	for _, s := range base {
		bCount[s]++
	}
	for i, s := range mod {
		mCount[s]++
		mIndex[s] = i
	}
	var bOut, mOut []int
	lastM := -1
	for bIdx, s := range base {
		if bCount[s] == 1 && mCount[s] == 1 {
			if mIdx := mIndex[s]; mIdx > lastM {
				bOut = append(bOut, bIdx)
				mOut = append(mOut, mIdx)
				lastM = mIdx
			}
		}
	}
	return bOut, mOut
}

func combineLineEdits(base []string, tEdits, wEdits []lineEdit) ([]byte, bool) {
	var out []string
	bPos, ti, wi := 0, 0, 0
	for ti < len(tEdits) || wi < len(wEdits) {
		var nextT, nextW *lineEdit
		if ti < len(tEdits) {
			nextT = &tEdits[ti]
		}
		if wi < len(wEdits) {
			nextW = &wEdits[wi]
		}
		if nextT != nil && nextW != nil && editsOverlap(*nextT, *nextW) {
			if nextT.startIdx == nextW.startIdx && nextT.endIdx == nextW.endIdx && slicesEqual(nextT.repl, nextW.repl) {
				out = append(out, base[bPos:nextT.startIdx]...)
				out = append(out, nextT.repl...)
				bPos = nextT.endIdx
				ti++
				wi++
				continue
			}
			return nil, false
		}
		if nextW == nil || (nextT != nil && nextT.startIdx < nextW.startIdx) {
			out = append(out, base[bPos:nextT.startIdx]...)
			out = append(out, nextT.repl...)
			bPos = nextT.endIdx
			ti++
		} else {
			out = append(out, base[bPos:nextW.startIdx]...)
			out = append(out, nextW.repl...)
			bPos = nextW.endIdx
			wi++
		}
	}
	out = append(out, base[bPos:]...)
	return []byte(strings.Join(out, "")), true
}

func editsOverlap(a, b lineEdit) bool {
	if a.startIdx == b.startIdx && a.endIdx == b.endIdx {
		return true
	}
	aEnd := a.endIdx
	bEnd := b.endIdx
	if a.startIdx == aEnd && b.startIdx == bEnd {
		return a.startIdx == b.startIdx
	}
	if a.startIdx == aEnd {
		return a.startIdx >= b.startIdx && a.startIdx <= bEnd
	}
	if b.startIdx == bEnd {
		return b.startIdx >= a.startIdx && b.startIdx <= aEnd
	}
	return a.startIdx < bEnd && b.startIdx < aEnd
}

func splitLinesKeepTerm(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func targetMatchesBase(cur TargetObservation, base layeredfs.BaseCapture) bool {
	if cur.Kind != base.Kind {
		return false
	}
	switch base.Kind {
	case layeredfs.EntryAbsent, layeredfs.EntryDirectory:
		return true
	case layeredfs.EntryRegular:
		return cur.SHA256 == base.SHA256 && cur.Size == base.Size
	case layeredfs.EntrySymlink:
		return cur.LinkTarget == base.LinkTarget
	default:
		return false
	}
}

func newConflict(entry ManifestEntry, cur TargetObservation, kind ConflictKind, msg string) *PromotionConflict {
	return &PromotionConflict{
		Path:                 entry.Path,
		Kind:                 kind,
		Message:              msg,
		Base:                 entry.Base,
		TargetObservation:    cur,
		WorkerFinal:          entry.Final,
		SupportedResolutions: defaultResolutions(),
	}
}

func containsBytesSlice(list [][]byte, candidate []byte) bool {
	for _, item := range list {
		if bytes.Equal(item, candidate) {
			return true
		}
	}
	return false
}
