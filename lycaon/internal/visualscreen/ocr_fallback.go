//go:build !darwin || !cgo

package visualscreen

import (
	"context"
)

type unavailableOCR struct{}

// NewDefaultOCREngine reports OCR as unavailable; only macOS builds with cgo
// link a recognizer.
func NewDefaultOCREngine() OCREngine {
	return unavailableOCR{}
}

func (unavailableOCR) RecognizeText(context.Context, string, []byte) ([]TextSpan, error) {
	return nil, ErrOCRUnavailable
}
