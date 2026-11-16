package briefing

import "strings"

// SourceReasoning is the source kind of the model's own reasoning excerpt.
const SourceReasoning = "reasoning"

// Evidence quality values a reasoning item implies, as the wire declares them.
const (
	QualityReasoningIncluded    = "reasoning_included"
	QualityReasoningTruncated   = "reasoning_truncated"
	QualityReasoningShared      = "reasoning_shared"
	QualityReasoningUnavailable = "reasoning_unavailable"
)

const reasoningItemKey = "assistant_reasoning"

// reasoningQuestions are the templates whose question is about intent. Only
// they may carry a reasoning item; Canonical refuses it anywhere else.
var reasoningQuestions = map[string]bool{
	purposeTemplate.ID: true,
}

// ReasoningEvidence is a section's reasoning item, the text its prompt
// receives, and the quality flags the input implies.
type ReasoningEvidence struct {
	Item    Item
	Text    string
	Quality []string
}

// ReasoningItem is the only builder of a reasoning item. Its flags come from
// the typed input, never from the item's reason text.
func ReasoningItem(in ReasoningInput, audience string) ReasoningEvidence {
	text, ref := "", ""
	if in.Excerpt != nil {
		text, ref = in.Excerpt.Text, in.Excerpt.Ref
	}
	item, clamped := boundedItem(reasoningItemKey, RoleContext, SourceReasoning, ref, text, sectionRunes, in.Gap, audience)
	var quality []string
	sent := item.Inclusion == IncludedWhole || item.Inclusion == IncludedTruncated
	if excerpt := in.Excerpt; excerpt != nil && sent {
		if excerpt.Shared || excerpt.Truncated {
			item.Reason = strings.Join(nonEmpty(flag(excerpt.Shared, "shared across calls"), flag(excerpt.Truncated, "suffix")), ", ")
		}
		quality = append(quality, QualityReasoningIncluded)
		if excerpt.Truncated || item.Inclusion == IncludedTruncated {
			quality = append(quality, QualityReasoningTruncated)
		}
		if excerpt.Shared {
			quality = append(quality, QualityReasoningShared)
		}
	} else if in.Gap != "" {
		quality = append(quality, QualityReasoningUnavailable)
	}
	return ReasoningEvidence{Item: item, Text: clamped, Quality: quality}
}
