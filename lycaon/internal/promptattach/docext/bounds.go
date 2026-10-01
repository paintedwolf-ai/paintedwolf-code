package docext

import (
	"context"
	"fmt"
	"time"
)

// Bounds limits document extraction.
type Bounds struct {
	MaxPages             int
	MaxSlides            int
	MaxParse             time.Duration
	MaxExpansionRatio    int
	MaxBodyBytes         int64
	MaxExtractedBytes    int
	MaxWorkerMemoryBytes int64
}

// Validate checks every extraction bound.
func (b Bounds) Validate() error {
	values := []struct {
		name  string
		value int64
	}{
		{"max_pages", int64(b.MaxPages)},
		{"max_slides", int64(b.MaxSlides)},
		{"max_expansion_ratio", int64(b.MaxExpansionRatio)},
		{"max_body_bytes", b.MaxBodyBytes},
		{"max_extracted_bytes", int64(b.MaxExtractedBytes)},
		{"max_worker_memory_bytes", b.MaxWorkerMemoryBytes},
		{"max_parse", int64(b.MaxParse)},
	}
	for _, bound := range values {
		if bound.value <= 0 {
			return fmt.Errorf("%s must be positive", bound.name)
		}
	}
	return nil
}

// WithTimeout derives a child context bounded by MaxParse.
func (b Bounds) WithTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, b.MaxParse)
}
