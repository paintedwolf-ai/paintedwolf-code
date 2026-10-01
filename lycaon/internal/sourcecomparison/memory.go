package sourcecomparison

import (
	"strings"
	"unsafe"

	"github.com/lycaon/lycaon/pkg/api"
)

const maxInlineReservation = 2 << 20

func inlineReservation(before, after api.SourceComparisonSide) int64 {
	if before.Content == after.Content {
		return 0
	}
	return min(maxInlineReservation, 4096+16*int64(len(before.Content)+len(after.Content)))
}

// Preparation reserves the largest transient allocation phase.
func comparisonReservation(before, after api.SourceComparisonSide, attribution *api.SourceComparisonAttribution) int64 {
	bytes := int64(len(before.Content) + len(after.Content))
	count := int64(strings.Count(before.Content, "\n") + strings.Count(after.Content, "\n") + 2)
	if before.Content == after.Content {
		return inlineReservation(before, after) + 2*bytes + 24*count + metadataBytes(before, after, attribution)
	}
	return inlineReservation(before, after) + 4*bytes + 192*count + metadataBytes(before, after, attribution)
}
func metadataBytes(before, after api.SourceComparisonSide, attribution *api.SourceComparisonAttribution) int64 {
	size := int64(4096)
	for _, side := range []api.SourceComparisonSide{before, after} {
		size += int64(len(side.Path) + len(side.Sha256) + len(side.VersionID) + len(side.RootID) + len(side.Reason))
	}
	if attribution != nil {
		for _, ranges := range [][]api.SourceAttributedText{attribution.Before, attribution.After} {
			size += int64(cap(ranges)) * int64(unsafe.Sizeof(api.SourceAttributedText{}))
			for _, part := range ranges {
				size += int64(cap(part.Contributors)) * int64(unsafe.Sizeof(api.SourceContributor{}))
				for _, author := range part.Contributors {
					size += int64(len(author.Origin) + len(author.SessionID) + len(author.PersonID) + len(author.ActorLabel) + len(author.ToolCallID) + len(author.ToolName) + len(author.WorkerID))
				}
			}
		}
	}
	return size
}
func (d *Document) retainedBytes() int64 {
	// Normalization can retain one additional backing string per endpoint.
	size := int64(unsafe.Sizeof(*d)) + inlineReservation(d.Before, d.After) + 2*int64(len(d.Before.Content)+len(d.After.Content)) + metadataBytes(d.Before, d.After, d.Attribution)
	size += int64(cap(d.rows)) * int64(unsafe.Sizeof(planRow{}))
	size += int64(cap(d.groups)) * int64(unsafe.Sizeof(changeGroup{}))
	size += int64(cap(d.beforeRows)+cap(d.afterRows)) * int64(unsafe.Sizeof(int(0)))
	size += int64(cap(d.Summary.ChangeAreas)) * int64(unsafe.Sizeof(api.SourceReaderChangeArea{}))
	size += int64(cap(d.unified.Hunks)) * int64(unsafe.Sizeof(uintptr(0)))
	for _, hunk := range d.unified.Hunks {
		size += int64(unsafe.Sizeof(*hunk))
		if len(hunk.Lines) > 0 {
			size += int64(cap(hunk.Lines)) * int64(unsafe.Sizeof(hunk.Lines[0]))
		}
	}
	for _, side := range []decoratedSide{d.beforePlan, d.afterPlan} {
		size += int64(cap(side.lines)) * int64(unsafe.Sizeof(""))
		size += int64(cap(side.coordinates)) * int64(unsafe.Sizeof(sourceCoordinate{}))
		size += int64(cap(side.syntax.data)) + int64(cap(side.syntax.blocks))*int64(unsafe.Sizeof(syntaxBlock{}))
	}
	return size
}
func (d *Document) planReservation() int64 {
	return d.retainedBytes() + int64(d.Summary.Rows)*int64(unsafe.Sizeof(planRow{})) + int64(d.Summary.Before.Lines+d.Summary.After.Lines)*32 + int64(len(d.unified.Hunks))*64
}
func (d *Document) decorationReservation() int64 {
	a, b := int64(len(d.Before.Content)), int64(len(d.After.Content))
	// Only one lexer runs at a time; the other side retains its compact token index.
	return d.retainedBytes() + 8*max(a, b) + 4*min(a, b) + int64(d.Summary.Before.Lines+d.Summary.After.Lines)*32
}
