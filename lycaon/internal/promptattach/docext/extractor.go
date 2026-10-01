package docext

import (
	"context"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/promptattach/attacherr"

	"github.com/lycaon/lycaon/internal/promptattach/docformat"
)

// Request is bytes-only document intake for one attachment.
type Request struct {
	Filename string
	MIME     string
	Bytes    []byte
}

type boundedRequest struct {
	Filename string
	MIME     string
	Bytes    []byte
	Bounds   Bounds
}

// DocumentExtractor applies one extraction budget.
type DocumentExtractor struct {
	bounds Bounds
}

// New returns a document extractor.
func New(bounds Bounds) *DocumentExtractor {
	return &DocumentExtractor{bounds: bounds}
}

// Extract routes by detected format.
func (e *DocumentExtractor) Extract(ctx context.Context, req Request) (Result, error) {
	if e == nil {
		return Result{}, attacherr.Unsupported("document extractor not configured")
	}
	if len(req.Bytes) == 0 {
		return Result{}, attacherr.Unsupported("document bytes are empty")
	}
	bounds := e.bounds
	if err := bounds.Validate(); err != nil {
		return Result{}, fmt.Errorf("invalid document extraction bounds: %w", err)
	}
	if int64(len(req.Bytes)) > bounds.MaxBodyBytes {
		return Result{}, attacherr.TooLarge("document exceeds the materialization bound")
	}
	format, ok := docformat.Detect(req.Filename, req.MIME, req.Bytes)
	if !ok {
		return Result{}, attacherr.Unsupported("unsupported document format")
	}
	bounded := boundedRequest{
		Filename: req.Filename, MIME: req.MIME, Bytes: req.Bytes,
		Bounds: bounds,
	}
	ctx, cancel := bounds.WithTimeout(ctx)
	defer cancel()
	out, err := extractDocument(ctx, format, bounded)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return Result{}, attacherr.TooLarge("document parse exceeded time bound")
		}
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, attacherr.TooLarge("document parse exceeded time bound")
	}
	out.Text = truncateExtractedText(out.Text, bounds.MaxExtractedBytes)
	return out, nil
}

func extractDocument(ctx context.Context, format docformat.Format, req boundedRequest) (Result, error) {
	switch format {
	case docformat.PDF, docformat.DOCX, docformat.XLSX, docformat.PPTX, docformat.ODT:
		return extractTabula(ctx, format, req)
	case docformat.ODS, docformat.ODP:
		return extractODF(ctx, format, req)
	case docformat.RTF:
		return extractRTF(ctx, req)
	default:
		return Result{}, attacherr.Unsupported(fmt.Sprintf("unsupported document format %s", format))
	}
}
