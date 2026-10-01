package promptattach

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/promptattach/attacherr"
	"github.com/lycaon/lycaon/internal/promptattach/docext"
	"github.com/lycaon/lycaon/internal/promptattach/format"
	"github.com/lycaon/lycaon/pkg/api"
)

// FramedPart combines a prompt fence with typed display identity.
type FramedPart struct {
	SourceContext *api.SourceContext
	Fence         string
	Source        string
	MediaType     string
	// BlobID identifies a staged payload.
	BlobID string
	// Path is empty when no materialized body exists.
	Path string
	// RootID is the attached root a path reference resolves under.
	RootID string
	// SizeBytes is the full decoded body size for a payload attachment.
	SizeBytes int64
	// ReferenceKind is empty for payloads.
	ReferenceKind api.MessageReferenceKind
	// HitKind and SourceRef identify a resolved search hit.
	HitKind   string
	SourceRef string
	// SourceSessionID is the evidence row's session coordinate.
	SourceSessionID string
	// StartLine and EndLine bound a path-file reference; zero means whole file.
	StartLine int
	EndLine   int
}

// Fences returns the model-facing fence strings in order.
func Fences(parts []FramedPart) []string {
	if len(parts) == 0 {
		return nil
	}
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = p.Fence
	}
	return out
}

// IngestResult carries framed text and attached images.
type IngestResult struct {
	Parts         []FramedPart
	Images        []InlineImage
	ScannedNoText bool
}

// Fences returns model-facing fence strings for the ingested attachments.
func (r IngestResult) Fences() []string { return Fences(r.Parts) }

// InlineImage is a materialized raster.
type InlineImage struct {
	Mime  string
	Bytes []byte
}

// IngestAttachments frames attachments within the turn preview budget.
func IngestAttachments(
	ctx context.Context,
	store blobstore.Store,
	caps Caps,
	previewBudget *TurnPreviewBudget,
	video VideoDecoder,
	parts []api.PromptAttachmentPart,
) (IngestResult, error) {
	if len(parts) == 0 {
		return IngestResult{}, nil
	}
	extractor := docext.New(caps.DocumentBounds())
	out := IngestResult{Parts: make([]FramedPart, 0, len(parts))}
	turnBytes := int64(0)

	for _, part := range parts {
		blob, err := store.Resolve(strings.TrimSpace(part.BlobID))
		if err != nil {
			return IngestResult{}, resolveError(part.BlobID, err)
		}
		turnBytes += blob.Size
		if turnBytes > caps.Materialization.MaxTurn.Int64() {
			return IngestResult{}, attacherr.TooLarge("attachments on one turn exceed the materialization bound")
		}
		detected, err := detectBlob(store, blob)
		if err != nil {
			return IngestResult{}, err
		}
		if err := projectBlob(ctx, projection{
			store:         store,
			caps:          caps,
			extractor:     extractor,
			blob:          blob,
			detected:      detected,
			previewBudget: previewBudget,
			video:         video,
		}, &out); err != nil {
			return IngestResult{}, err
		}
	}
	return out, nil
}

type projection struct {
	store         blobstore.Store
	caps          Caps
	extractor     *docext.DocumentExtractor
	blob          blobstore.Blob
	detected      format.Format
	previewBudget *TurnPreviewBudget
	video         VideoDecoder
}

func projectBlob(ctx context.Context, p projection, out *IngestResult) error {
	switch p.detected.Kind {
	case format.KindText:
		fence, err := frameTextBlob(p.store, p.blob, p.detected.MIME, p.previewBudget.claimText(p.blob.Size))
		if err != nil {
			return err
		}
		out.Parts = append(out.Parts, framedPayload(p.blob, p.detected.MIME, fence))
		return nil

	case format.KindDocument:
		return projectDocument(ctx, p, out)

	case format.KindImage:
		raw, err := readBlob(p.store, p.blob, p.caps.Transport.MaxImage.Int64())
		if err != nil {
			return err
		}
		out.Images = append(out.Images, InlineImage{Mime: p.detected.MIME, Bytes: raw})
		return nil

	case format.KindVideo:
		return projectVideo(ctx, p, out)

	case format.KindContainer, format.KindUnsupported:
	}
	return attacherr.Unsupported(fmt.Sprintf("attachment %q is not text-family, document, image, or video", p.blob.Name))
}

func projectDocument(ctx context.Context, p projection, out *IngestResult) error {
	if p.blob.Size > p.caps.Document.MaxBody.Int64() {
		return attacherr.TooLarge(fmt.Sprintf("%q is larger than the host will parse as a document", p.blob.Name))
	}
	raw, err := readBlob(p.store, p.blob, p.caps.Document.MaxBody.Int64())
	if err != nil {
		return err
	}
	res, err := p.extractor.Extract(ctx, docext.Request{
		Filename: p.blob.Name,
		MIME:     p.detected.MIME,
		Bytes:    raw,
	})
	if err != nil {
		return err
	}
	if res.ScannedNoText {
		out.ScannedNoText = true
	}
	out.Parts = append(out.Parts, framedPayload(p.blob, p.detected.MIME,
		frameExtract(p.blob, p.detected.MIME, res.Text, p.previewBudget.claimBody(int64(len(res.Text))), unitAttrs(p.detected.MIME, res.UnitCount)...)))
	return nil
}

// detectBlob determines type from stored bytes.
func detectBlob(store blobstore.Store, blob blobstore.Blob) (format.Format, error) {
	f, err := store.Open(blob)
	if err != nil {
		return format.Format{}, attacherr.Unsupported(fmt.Sprintf("attachment %q could not be read", blob.Name))
	}
	defer func() { _ = f.Close() }()
	peek := make([]byte, format.PeekBytes)
	n, err := io.ReadFull(f, peek)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return format.Format{}, attacherr.Unsupported(fmt.Sprintf("attachment %q could not be read", blob.Name))
	}
	if n == 0 {
		return format.Format{}, attacherr.Unsupported(fmt.Sprintf("attachment %q is empty", blob.Name))
	}
	return format.Detect(blob.Name, "", peek[:n]), nil
}

func readBlob(store blobstore.Store, blob blobstore.Blob, max int64) ([]byte, error) {
	if blob.Size > max {
		return nil, attacherr.TooLarge(fmt.Sprintf("%q exceeds the byte cap for its kind", blob.Name))
	}
	f, err := store.Open(blob)
	if err != nil {
		return nil, attacherr.Unsupported(fmt.Sprintf("attachment %q could not be read", blob.Name))
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(bufio.NewReader(f), max))
	if err != nil {
		return nil, attacherr.Unsupported(fmt.Sprintf("attachment %q could not be read", blob.Name))
	}
	return raw, nil
}

func framedPayload(blob blobstore.Blob, mime, fence string) FramedPart {
	return FramedPart{
		Fence:     fence,
		Source:    blob.Name,
		MediaType: mime,
		BlobID:    blob.ID,
		Path:      blob.Rel,
		SizeBytes: blob.Size,
	}
}

func resolveError(id string, err error) error {
	if errors.Is(err, blobstore.ErrNotFound) {
		return attacherr.NotFound(fmt.Sprintf("attachment %q is no longer staged; attach it again", strings.TrimSpace(id)))
	}
	if errors.Is(err, blobstore.ErrNoStore) {
		return attacherr.Unsupported("attachment storage is unavailable for this project")
	}
	return err
}

func unitAttrs(mime string, units int) []string {
	if units <= 0 {
		return nil
	}
	lower := strings.ToLower(mime)
	if strings.Contains(lower, "presentation") || strings.HasSuffix(lower, "odp") {
		return []string{fmt.Sprintf("slide=%q", fmt.Sprint(units))}
	}
	if strings.Contains(lower, "spreadsheet") || strings.HasSuffix(lower, "ods") || strings.Contains(lower, "sheet") {
		return []string{fmt.Sprintf("sheet=%q", fmt.Sprint(units))}
	}
	return []string{fmt.Sprintf("page=%q", fmt.Sprint(units))}
}

// EnforceCounts bounds attachments and references separately.
func EnforceCounts(caps Caps, attachmentCount, refCount int) error {
	if attachmentCount < 0 || refCount < 0 {
		return attacherr.TooLarge("invalid attachment count")
	}
	if attachmentCount > caps.Counts.MaxAttachments {
		return attacherr.TooLarge("too many attachments on one prompt")
	}
	if refCount > caps.Counts.MaxReferences {
		return attacherr.TooLarge("too many attachment references on one prompt")
	}
	return nil
}

// JoinUserText appends fenced attachment blocks to the user prose.
func JoinUserText(prose string, fences []string) string {
	prose = strings.TrimSpace(prose)
	if len(fences) == 0 {
		return prose
	}
	var b strings.Builder
	if prose != "" {
		b.WriteString(prose)
		b.WriteString("\n\n")
	}
	for i, fence := range fences {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(fence)
	}
	return b.String()
}
