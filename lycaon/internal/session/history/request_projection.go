package history

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

const sourceBriefPrefix = "host-source-changes-"

func isCompactionSummaryOrCheckpoint(row api.Message) bool {
	return row.CompactionCheckpoint || len(row.ContentParts) == 1 && row.ContentParts[0].Source == "compaction_summary"
}

func isCompactionHeadRow(row api.Message, viewByID map[string]api.Message) bool {
	if isCompactionSummaryOrCheckpoint(row) {
		return true
	}
	if row.Ord <= 0 {
		if viewMsg, ok := viewByID[row.ID]; ok && viewMsg.Ord <= 0 {
			return true
		}
	}
	return false
}

func isProjectedHistoryMetadata(row api.Message) bool {
	return isCompactionSummaryOrCheckpoint(row) ||
		row.ID == historyCoverageNoticeID ||
		row.ID == "host-context-trim-notice" ||
		row.ID == "host-skill-procedure" ||
		row.ID == "host-skill-pointer"
}

type interleavedFrameRow struct {
	row      api.Message
	anchorID string
	afterIDs map[string]bool
}

// requestProjection separates canonical history from the live request frame.
type requestProjection struct {
	messages    []api.Message
	history     []api.Message
	frame       []api.Message
	prefixFrame []api.Message
	interleaved []interleavedFrameRow
	suffixFrame []api.Message
	ids         map[string]bool
}

func projectRequest(messages, history []api.Message) requestProjection {
	p := requestProjection{messages: messages, ids: make(map[string]bool, len(history))}
	for _, row := range history {
		if row.ID != "" {
			p.ids[row.ID] = true
		}
	}
	firstHistIdx, lastHistIdx := -1, -1
	for i, row := range messages {
		if isProjectedHistoryMetadata(row) {
			p.ids[row.ID] = true
		}
		if p.ids[row.ID] {
			if firstHistIdx < 0 {
				firstHistIdx = i
			}
			lastHistIdx = i
			p.history = append(p.history, row)
		} else {
			p.frame = append(p.frame, row)
		}
	}
	if firstHistIdx < 0 {
		p.prefixFrame = append(p.prefixFrame, p.frame...)
		return p
	}
	for i := 0; i <= lastHistIdx; i++ {
		row := messages[i]
		if p.ids[row.ID] {
			continue
		}
		if i < firstHistIdx && !strings.HasPrefix(row.ID, sourceBriefPrefix) {
			p.prefixFrame = append(p.prefixFrame, row)
			continue
		}
		anchorID := strings.TrimPrefix(row.ID, sourceBriefPrefix)
		if anchorID == row.ID {
			anchorID = ""
		}
		after := make(map[string]bool)
		for j := i + 1; j <= lastHistIdx; j++ {
			if p.ids[messages[j].ID] && messages[j].ID != "" {
				if anchorID == "" {
					anchorID = messages[j].ID
				}
				after[messages[j].ID] = true
			}
		}
		p.interleaved = append(p.interleaved, interleavedFrameRow{
			row:      row,
			anchorID: anchorID,
			afterIDs: after,
		})
	}
	p.suffixFrame = append(p.suffixFrame, messages[lastHistIdx+1:]...)
	return p
}

// replace keeps frame rows in their original order and restores fitted history inside the history slot.
func (p requestProjection) replace(history []api.Message) []api.Message {
	out := make([]api.Message, 0, len(p.frame)+len(history))
	out = append(out, p.prefixFrame...)
	if len(p.interleaved) == 0 {
		out = append(out, history...)
		return append(out, p.suffixFrame...)
	}
	present := make(map[string]bool, len(history))
	for _, row := range history {
		if row.ID != "" {
			present[row.ID] = true
		}
	}
	emitted := make([]bool, len(p.interleaved))
	for _, row := range history {
		for idx, item := range p.interleaved {
			if emitted[idx] {
				continue
			}
			if item.anchorID != "" && present[item.anchorID] {
				if row.ID == item.anchorID {
					out = append(out, item.row)
					emitted[idx] = true
				}
				continue
			}
			if item.afterIDs[row.ID] {
				out = append(out, item.row)
				emitted[idx] = true
			}
		}
		out = append(out, row)
	}
	for idx, item := range p.interleaved {
		if !emitted[idx] {
			out = append(out, item.row)
		}
	}
	return append(out, p.suffixFrame...)
}
