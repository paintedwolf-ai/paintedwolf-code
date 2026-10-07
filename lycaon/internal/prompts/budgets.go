package prompts

import (
	"fmt"

	"github.com/lycaon/lycaon/config"
)

// PromptBudgets bounds rendered prompts, attachments, and perception.
type PromptBudgets struct {
	Version int `yaml:"version"`
	// Sizes holds rendered prompt artifacts to their category limits.
	Sizes PromptSizes `yaml:"sizes"`
	// AbsoluteMaximums bounds static prompts against the model window.
	AbsoluteMaximums  *AbsoluteMaximums  `yaml:"absolute_maximums,omitempty"`
	PromptAttachments *PromptAttachments `yaml:"prompt_attachments,omitempty"`
	// AgentsMDInject caps each policy file.
	AgentsMDInject *AgentsMDInject `yaml:"agents_md_inject,omitempty"`
	// Perception bounds the tool-result images one request attaches.
	Perception *Perception `yaml:"perception,omitempty"`
}

// Perception bounds tool-result pixels per request. Once max_tool_images
// is exceeded, the oldest drop_batch images leave together.
type Perception struct {
	MaxToolImages int `yaml:"max_tool_images"`
	DropBatch     int `yaml:"drop_batch"`
}

// AgentsMDInject caps AGENTS.md chain-inject body size (UTF-8 bytes).
type AgentsMDInject struct {
	MaxBodyBytes int `yaml:"max_body_bytes"`
}

// PromptAttachments groups attachment limits by resource plane.
type PromptAttachments struct {
	Composer        AttachmentComposer        `yaml:"composer"`
	Counts          AttachmentCounts          `yaml:"counts"`
	Transport       AttachmentTransport       `yaml:"transport"`
	Materialization AttachmentMaterialization `yaml:"materialization"`
	Prompt          AttachmentPrompt          `yaml:"prompt"`
	Document        AttachmentDocument        `yaml:"document"`
	Video           AttachmentVideo           `yaml:"video"`
}

// AttachmentComposer bounds inline prompt text.
type AttachmentComposer struct {
	AutoAttachPasteBytes int `yaml:"auto_attach_paste_bytes"`
	MaxInlineTextBytes   int `yaml:"max_inline_text_bytes"`
}

// AttachmentCounts bounds how many of each intake kind ride one turn.
type AttachmentCounts struct {
	MaxAttachments int `yaml:"max_attachments"`
	MaxReferences  int `yaml:"max_references"`
	MaxImages      int `yaml:"max_images"`
}

// AttachmentTransport bounds bytes accepted off the wire.
type AttachmentTransport struct {
	// MaxUploadBytes bounds one streamed attachment upload body.
	MaxUploadBytes int `yaml:"max_upload_bytes"`
	// MaxPromptRequestBytes bounds the prompt route's JSON envelope, which
	// carries handles and prose rather than attachment payloads.
	MaxPromptRequestBytes int `yaml:"max_prompt_request_bytes"`
	MaxImageBytes         int `yaml:"max_image_bytes"`
}

// AttachmentMaterialization bounds bytes permitted to land on disk.
type AttachmentMaterialization struct {
	MaxBodyBytes      int `yaml:"max_body_bytes"`
	MaxTurnBytes      int `yaml:"max_turn_bytes"`
	MaxExpansionRatio int `yaml:"max_expansion_ratio"`
	MaxUnwrapDepth    int `yaml:"max_unwrap_depth"`
}

// AttachmentPrompt bounds model-visible attachment text.
type AttachmentPrompt struct {
	MaxBodyPreviewBytes      int `yaml:"max_body_preview_bytes"`
	MaxLargeTextPreviewBytes int `yaml:"max_large_text_preview_bytes"`
	MaxTurnPreviewBytes      int `yaml:"max_turn_preview_bytes"`
}

// AttachmentDocument bounds isolated document extraction.
type AttachmentDocument struct {
	MaxBodyBytes         int   `yaml:"max_body_bytes"`
	MaxExtractedBytes    int   `yaml:"max_extracted_bytes"`
	MaxWorkerMemoryBytes int64 `yaml:"max_worker_memory_bytes"`
	MaxPages             int   `yaml:"max_pages"`
	MaxSlides            int   `yaml:"max_slides"`
	MaxParseSeconds      int   `yaml:"max_parse_seconds"`
}

// AttachmentVideo bounds videos decoded for their frames.
type AttachmentVideo struct {
	MaxBodyBytes int `yaml:"max_body_bytes"`
}

// PromptSizes limits rendered prompt artifacts in UTF-8 bytes by category:
// worker_personas, coordinator_tripartite, coordinator_injects,
// agent_templates, kicks, and tool_surfaces.
type PromptSizes struct {
	Limits map[string]SizeLimit `yaml:"limits"`
	// Grandfathered caps record artifacts that predate their limit; they only shrink.
	Grandfathered map[string]map[string]int `yaml:"grandfathered"`
	// Exceptions admit artifacts above their limit for a stated reason.
	Exceptions map[string]map[string]SizeException `yaml:"exceptions"`
}

// SizeLimit is a category's warning line and hard limit.
type SizeLimit struct {
	Warn  int `yaml:"warn"`
	Limit int `yaml:"limit"`
}

// SizeException admits one artifact above its category limit.
type SizeException struct {
	Cap    int    `yaml:"cap"`
	Reason string `yaml:"reason"`
}

// Cap returns the most one artifact of a category may measure.
func (s PromptSizes) Cap(category, id string) int {
	if exception, ok := s.Exceptions[category][id]; ok {
		return exception.Cap
	}
	if grandfathered, ok := s.Grandfathered[category][id]; ok {
		return grandfathered
	}
	return s.Limits[category].Limit
}

// AbsoluteMaximums records model-window token ceilings.
type AbsoluteMaximums struct {
	Model string `yaml:"model,omitempty"`
	// ModelContextWindowTokens bounds one request.
	ModelContextWindowTokens int `yaml:"model_context_window_tokens"`
	// ReservedSessionTokens preserves live turn capacity.
	ReservedSessionTokens int `yaml:"reserved_session_tokens"`
}

// StaticStackCeilingTokens returns the static prompt budget.
func (a *AbsoluteMaximums) StaticStackCeilingTokens() int {
	if a == nil {
		return 0
	}
	return a.ModelContextWindowTokens - a.ReservedSessionTokens
}

// LoadPromptBudgets reads the bundled prompt-budgets.yaml.
func LoadPromptBudgets() (*PromptBudgets, error) {
	raw, err := config.Read(config.PromptBudgets)
	if err != nil {
		return nil, fmt.Errorf("read prompt budgets: %w", err)
	}
	return decodePromptBudgets(raw)
}

func decodePromptBudgets(raw []byte) (*PromptBudgets, error) {
	var cfg PromptBudgets
	if err := config.DecodeYAML(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse prompt budgets: %w", err)
	}
	if cfg.Version <= 0 {
		return nil, fmt.Errorf("prompt budgets: version must be positive")
	}
	if limit := cfg.Sizes.Limits["worker_personas"]; limit.Limit <= 0 {
		return nil, fmt.Errorf("prompt budgets: sizes.limits.worker_personas.limit must be positive")
	}
	if am := cfg.AbsoluteMaximums; am != nil {
		if am.ModelContextWindowTokens <= 0 {
			return nil, fmt.Errorf("prompt budgets: absolute_maximums.model_context_window_tokens must be positive")
		}
		if am.ReservedSessionTokens < 0 || am.ReservedSessionTokens >= am.ModelContextWindowTokens {
			return nil, fmt.Errorf("prompt budgets: absolute_maximums.reserved_session_tokens must be in [0, model_context_window_tokens)")
		}
	}
	if cfg.AgentsMDInject == nil || cfg.AgentsMDInject.MaxBodyBytes <= 0 {
		return nil, fmt.Errorf("prompt budgets: agents_md_inject.max_body_bytes must be positive")
	}
	if p := cfg.Perception; p == nil || p.MaxToolImages <= 0 || p.DropBatch <= 0 || p.DropBatch > p.MaxToolImages {
		return nil, fmt.Errorf("prompt budgets: perception.max_tool_images must be positive and perception.drop_batch in [1, max_tool_images]")
	}
	return &cfg, nil
}
