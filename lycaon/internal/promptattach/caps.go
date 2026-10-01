// Package promptattach handles prompt attachment intake.
package promptattach

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/promptattach/docext"
	"github.com/lycaon/lycaon/internal/prompts"
)

// StagedTTL is the abandoned-upload collection horizon.
const StagedTTL = 24 * time.Hour

// Caps is the process-wide attachment policy.
type Caps struct {
	Composer        ComposerCaps
	Counts          Counts
	Transport       TransportCaps
	Materialization MaterializationCaps
	Prompt          PromptCaps
	Document        DocumentCaps
	Video           VideoCaps
}

// ComposerCaps bounds inline prompt text.
type ComposerCaps struct {
	AutoAttachPaste bytebound.Prompt
	MaxInlineText   bytebound.Prompt
}

// Counts bounds how many of each intake kind ride one turn.
type Counts struct {
	MaxAttachments int
	MaxReferences  int
	MaxImages      int
}

// TransportCaps bounds bytes accepted off the wire.
type TransportCaps struct {
	MaxUpload        bytebound.Transport
	MaxPromptRequest bytebound.Transport
	MaxImage         bytebound.Transport
}

// MaterializationCaps bounds bytes permitted to land on disk.
type MaterializationCaps struct {
	MaxBody bytebound.Materialization
	MaxTurn bytebound.Materialization
	// MaxExpansionRatio bounds decompressed output against compressed input.
	MaxExpansionRatio int
	// MaxUnwrapDepth bounds nested container decoding.
	MaxUnwrapDepth int
}

// PromptCaps bounds bytes permitted into the model context.
type PromptCaps struct {
	MaxBodyPreview      bytebound.Prompt
	MaxLargeTextPreview bytebound.Prompt
	MaxTurnPreview      bytebound.Prompt
}

// DocumentCaps bounds isolated document extraction.
type DocumentCaps struct {
	MaxBody         bytebound.Materialization
	MaxExtracted    bytebound.Prompt
	MaxWorkerMemory int64
	MaxPages        int
	MaxSlides       int
	MaxParse        time.Duration
}

// VideoCaps bounds videos the managed browser decodes.
type VideoCaps struct {
	// MaxBody bounds one video, which is held in memory while the browser reads it.
	MaxBody bytebound.Materialization
}

// active serves capture paths without a request-scoped policy.
var active atomic.Pointer[Caps]

func init() {
	caps, err := LoadCaps()
	if err != nil {
		panic(fmt.Sprintf("load bundled prompt attachment caps: %v", err))
	}
	Install(caps)
}

// LoadCaps reads and validates the bundled attachment policy.
func LoadCaps() (Caps, error) {
	budgets, err := prompts.LoadPromptBudgets()
	if err != nil {
		return Caps{}, fmt.Errorf("load prompt attachment caps: %w", err)
	}
	if budgets.PromptAttachments == nil {
		return Caps{}, fmt.Errorf("load prompt attachment caps: prompt_attachments is required")
	}
	caps := capsFromConfig(budgets.PromptAttachments)
	if err := caps.Validate(); err != nil {
		return Caps{}, err
	}
	return caps, nil
}

// Install publishes caps for the boot-wired read-only consumers.
func Install(c Caps) { active.Store(&c) }

// Active returns the boot-loaded policy for capture surfaces.
func Active() Caps {
	if c := active.Load(); c != nil {
		return *c
	}
	return Caps{}
}

// Validate checks cross-plane cap ordering.
func (c Caps) Validate() error {
	positive := []struct {
		name string
		n    int
	}{
		{"counts.max_attachments", c.Counts.MaxAttachments},
		{"composer.auto_attach_paste_bytes", int(c.Composer.AutoAttachPaste)},
		{"composer.max_inline_text_bytes", int(c.Composer.MaxInlineText)},
		{"counts.max_references", c.Counts.MaxReferences},
		{"counts.max_images", c.Counts.MaxImages},
		{"transport.max_upload_bytes", int(c.Transport.MaxUpload)},
		{"transport.max_prompt_request_bytes", int(c.Transport.MaxPromptRequest)},
		{"transport.max_image_bytes", int(c.Transport.MaxImage)},
		{"materialization.max_body_bytes", int(c.Materialization.MaxBody)},
		{"materialization.max_turn_bytes", int(c.Materialization.MaxTurn)},
		{"materialization.max_expansion_ratio", c.Materialization.MaxExpansionRatio},
		{"materialization.max_unwrap_depth", c.Materialization.MaxUnwrapDepth},
		{"prompt.max_body_preview_bytes", int(c.Prompt.MaxBodyPreview)},
		{"prompt.max_large_text_preview_bytes", int(c.Prompt.MaxLargeTextPreview)},
		{"prompt.max_turn_preview_bytes", int(c.Prompt.MaxTurnPreview)},
		{"document.max_body_bytes", int(c.Document.MaxBody)},
		{"document.max_extracted_bytes", int(c.Document.MaxExtracted)},
		{"document.max_pages", c.Document.MaxPages},
		{"document.max_slides", c.Document.MaxSlides},
		{"document.max_parse_seconds", int(c.Document.MaxParse / time.Second)},
		{"video.max_body_bytes", int(c.Video.MaxBody)},
	}
	for _, f := range positive {
		if f.n <= 0 {
			return fmt.Errorf("prompt_attachments.%s must be positive, got %d", f.name, f.n)
		}
	}
	if c.Document.MaxWorkerMemory <= 0 {
		return fmt.Errorf("prompt_attachments.document.max_worker_memory_bytes must be positive, got %d", c.Document.MaxWorkerMemory)
	}

	ordered := []struct {
		lo, hi   string
		loN, hiN int64
	}{
		{"prompt.max_body_preview_bytes", "materialization.max_body_bytes", int64(c.Prompt.MaxBodyPreview), c.Materialization.MaxBody.Int64()},
		{"prompt.max_large_text_preview_bytes", "composer.auto_attach_paste_bytes", int64(c.Prompt.MaxLargeTextPreview), int64(c.Composer.AutoAttachPaste)},
		{"composer.auto_attach_paste_bytes", "composer.max_inline_text_bytes", int64(c.Composer.AutoAttachPaste), int64(c.Composer.MaxInlineText)},
		{"prompt.max_large_text_preview_bytes", "prompt.max_body_preview_bytes", int64(c.Prompt.MaxLargeTextPreview), int64(c.Prompt.MaxBodyPreview)},
		{"prompt.max_body_preview_bytes", "prompt.max_turn_preview_bytes", int64(c.Prompt.MaxBodyPreview), int64(c.Prompt.MaxTurnPreview)},
		{"transport.max_upload_bytes", "materialization.max_body_bytes", c.Transport.MaxUpload.Int64(), c.Materialization.MaxBody.Int64()},
		{"materialization.max_body_bytes", "materialization.max_turn_bytes", c.Materialization.MaxBody.Int64(), c.Materialization.MaxTurn.Int64()},
		{"transport.max_image_bytes", "transport.max_upload_bytes", c.Transport.MaxImage.Int64(), c.Transport.MaxUpload.Int64()},
		{"document.max_body_bytes", "materialization.max_body_bytes", c.Document.MaxBody.Int64(), c.Materialization.MaxBody.Int64()},
		{"document.max_body_bytes", "document.max_worker_memory_bytes", c.Document.MaxBody.Int64(), c.Document.MaxWorkerMemory},
		{"document.max_extracted_bytes", "document.max_worker_memory_bytes", int64(c.Document.MaxExtracted), c.Document.MaxWorkerMemory},
		{"video.max_body_bytes", "materialization.max_body_bytes", c.Video.MaxBody.Int64(), c.Materialization.MaxBody.Int64()},
	}
	for _, r := range ordered {
		if r.loN > r.hiN {
			return fmt.Errorf("prompt_attachments.%s (%d) must not exceed %s (%d)", r.lo, r.loN, r.hi, r.hiN)
		}
	}
	return nil
}

// DocumentBounds projects the document extraction budget.
func (c Caps) DocumentBounds() docext.Bounds {
	return docext.Bounds{
		MaxPages:             c.Document.MaxPages,
		MaxSlides:            c.Document.MaxSlides,
		MaxParse:             c.Document.MaxParse,
		MaxExpansionRatio:    c.Materialization.MaxExpansionRatio,
		MaxBodyBytes:         c.Document.MaxBody.Int64(),
		MaxExtractedBytes:    c.Document.MaxExtracted.Int(),
		MaxWorkerMemoryBytes: c.Document.MaxWorkerMemory,
	}
}

func capsFromConfig(cfg *prompts.PromptAttachments) Caps {
	return Caps{
		Composer: ComposerCaps{
			AutoAttachPaste: bytebound.Prompt(cfg.Composer.AutoAttachPasteBytes),
			MaxInlineText:   bytebound.Prompt(cfg.Composer.MaxInlineTextBytes),
		},
		Counts: Counts{
			MaxAttachments: cfg.Counts.MaxAttachments,
			MaxReferences:  cfg.Counts.MaxReferences,
			MaxImages:      cfg.Counts.MaxImages,
		},
		Transport: TransportCaps{
			MaxUpload:        bytebound.Transport(cfg.Transport.MaxUploadBytes),
			MaxPromptRequest: bytebound.Transport(cfg.Transport.MaxPromptRequestBytes),
			MaxImage:         bytebound.Transport(cfg.Transport.MaxImageBytes),
		},
		Materialization: MaterializationCaps{
			MaxBody:           bytebound.Materialization(cfg.Materialization.MaxBodyBytes),
			MaxTurn:           bytebound.Materialization(cfg.Materialization.MaxTurnBytes),
			MaxExpansionRatio: cfg.Materialization.MaxExpansionRatio,
			MaxUnwrapDepth:    cfg.Materialization.MaxUnwrapDepth,
		},
		Prompt: PromptCaps{
			MaxBodyPreview:      bytebound.Prompt(cfg.Prompt.MaxBodyPreviewBytes),
			MaxLargeTextPreview: bytebound.Prompt(cfg.Prompt.MaxLargeTextPreviewBytes),
			MaxTurnPreview:      bytebound.Prompt(cfg.Prompt.MaxTurnPreviewBytes),
		},
		Document: DocumentCaps{
			MaxBody:         bytebound.Materialization(cfg.Document.MaxBodyBytes),
			MaxExtracted:    bytebound.Prompt(cfg.Document.MaxExtractedBytes),
			MaxWorkerMemory: cfg.Document.MaxWorkerMemoryBytes,
			MaxPages:        cfg.Document.MaxPages,
			MaxSlides:       cfg.Document.MaxSlides,
			MaxParse:        time.Duration(cfg.Document.MaxParseSeconds) * time.Second,
		},
		Video: VideoCaps{MaxBody: bytebound.Materialization(cfg.Video.MaxBodyBytes)},
	}
}
