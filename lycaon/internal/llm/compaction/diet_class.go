package compaction

import (
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	DietEligibilityHot      = "hot"
	DietEligibilityPinned   = "pinned"
	DietEligibilityEligible = "eligible"
	boundedReadTokenCeiling = readcaps.LineLimit * 20
)

// DietStampPreserveStructure is the host override for structure-preserving compact.
const DietStampPreserveStructure = string(evidence.TranscriptDietPreserveStructure)

// Diet stamp sources (OpenAPI Message.diet_stamp_source).
const (
	DietStampSourceOverlayMerge = "overlay_merge"
	// The completion envelope survives deletion of its worker session.
	DietStampSourceWorkerEnvelope = "worker_envelope"
)

// MessageDietClass is the single gate result for transcript diet routing.
type MessageDietClass struct {
	Eligibility string
	Strategy    evidence.TranscriptDiet
}

// ClassifyMessageDietInput is the machine-state input for ClassifyMessageDiet.
type ClassifyMessageDietInput struct {
	Index    int
	Messages []ContextMessage
	Binding  *evidence.Binding
	// ForceEligible skips hot (commit-time wire compact).
	ForceEligible bool
}

// ClassifyMessageDiet applies diet eligibility then strategy.
// Hot is the current-turn tool row. Aged tool rows are eligible.
func ClassifyMessageDiet(in ClassifyMessageDietInput) MessageDietClass {
	if in.Index < 0 || in.Index >= len(in.Messages) {
		return MessageDietClass{Eligibility: DietEligibilityEligible, Strategy: evidence.TranscriptDietAgeDefault}
	}
	msg := in.Messages[in.Index]
	if msg.ContextPinned || msg.CompactionCheckpoint || msg.DietStampSource == DietStampSourceWorkerEnvelope {
		return MessageDietClass{Eligibility: DietEligibilityPinned}
	}
	if !in.ForceEligible {
		lastAssistant := lastAssistantIndex(in.Messages)
		if api.MessageRole(msg.Role) == api.MessageRoleTool && in.Index > lastAssistant {
			return MessageDietClass{Eligibility: DietEligibilityHot, Strategy: evidence.TranscriptDietAgeDefault}
		}
	}
	return MessageDietClass{Eligibility: DietEligibilityEligible, Strategy: resolveDietStrategy(msg, in.Binding)}
}

func lastAssistantIndex(messages []ContextMessage) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if api.MessageRole(messages[i].Role) == api.MessageRoleAssistant {
			return i
		}
	}
	return -1
}

func resolveDietStrategy(msg ContextMessage, binding *evidence.Binding) evidence.TranscriptDiet {
	stamp := strings.TrimSpace(msg.DietStamp)
	source := strings.TrimSpace(msg.DietStampSource)
	if stamp == DietStampPreserveStructure || source == DietStampSourceOverlayMerge {
		return evidence.TranscriptDietPreserveStructure
	}
	if binding == nil {
		binding = evidence.ActiveBinding()
	}
	toolName := strings.TrimSpace(msg.ToolName)
	if toolName == "" {
		return evidence.TranscriptDietAgeDefault
	}
	return binding.TranscriptDietForTool(toolName)
}

// SkipOversizedChunk preserves protected rows and tool-specific size allowances.
func (c MessageDietClass) SkipOversizedChunk(content string) bool {
	if c.Eligibility == DietEligibilityHot || c.Eligibility == DietEligibilityPinned {
		return true
	}
	switch c.Strategy {
	case evidence.TranscriptDietShapeBounded:
		return tokenest.EstimateDefault(content) < boundedReadTokenCeiling
	case evidence.TranscriptDietAnchorResidue:
		// Wire-fitted summarize packs stay inline; spill only above pack.wire_budget_tokens.
		return tokenest.EstimateDefault(content) <= summarizeCommitInlineCeiling()
	default:
		return false
	}
}
