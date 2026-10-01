//go:build darwin && cgo

package visualscreen

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework Vision
#include "ocr_darwin.h"
#include <stdlib.h>
*/
import "C"
import (
	"context"
	"errors"
	"unsafe"
)

type appleVisionOCR struct{}

// NewDefaultOCREngine returns an Apple Vision based OCR engine on macOS.
func NewDefaultOCREngine() OCREngine {
	return &appleVisionOCR{}
}

func (e *appleVisionOCR) RecognizeText(ctx context.Context, mime string, raw []byte) ([]TextSpan, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	cData := (*C.uchar)(unsafe.Pointer(&raw[0]))
	cLen := C.size_t(len(raw))

	res := C.recognize_text_apple_vision(cData, cLen)
	defer C.free_ocr_result(res)

	if res.error != nil {
		return nil, errors.New(C.GoString(res.error))
	}
	if res.count == 0 || res.spans == nil {
		return nil, nil
	}

	spansSlice := unsafe.Slice(res.spans, int(res.count))
	out := make([]TextSpan, 0, len(spansSlice))
	for _, s := range spansSlice {
		out = append(out, TextSpan{
			Text:       C.GoString(s.text),
			Confidence: float64(s.confidence),
			BoundingBox: [4]float64{
				float64(s.x),
				float64(s.y),
				float64(s.width),
				float64(s.height),
			},
		})
	}
	return out, nil
}
