package promptattach

import (
	"path"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// ForwardedKind identifies an openable attachment or path reference.
type ForwardedKind string

const (
	ForwardedPayload    ForwardedKind = "payload"
	ForwardedPathFile   ForwardedKind = "path_file"
	ForwardedPathFolder ForwardedKind = "path_folder"
)

// ForwardedAttachment identifies content available to a worker.
type ForwardedAttachment struct {
	Filename  string
	MIME      string
	Path      string
	SizeBytes int64
	Kind      ForwardedKind
	StartLine int
	EndLine   int
}

// Hint returns the worker's access instruction.
func (h ForwardedAttachment) Hint() string {
	if h.Kind == ForwardedPathFolder {
		return PathFolderHintPrefix + h.Path + "]"
	}
	if h.Kind == ForwardedPathFile {
		display := h.Path
		if h.StartLine >= 1 {
			if h.EndLine > h.StartLine {
				display = h.Path + ":" + strconv.Itoa(h.StartLine) + "-" + strconv.Itoa(h.EndLine)
			} else {
				display = h.Path + ":" + strconv.Itoa(h.StartLine)
			}
		}
		return PathFileHintPrefix + display + "]"
	}
	return BodyHint(h.MIME, h.Path)
}

// ForwardedAttachmentsFromMessage reads stamped attachment metadata.
func ForwardedAttachmentsFromMessage(msg api.Message) []ForwardedAttachment {
	return ForwardedAttachmentsFromParts(msg.ContentParts)
}

// ForwardedAttachmentsFromParts reads stamped attachment metadata.
func ForwardedAttachmentsFromParts(parts []api.MessageContentPart) []ForwardedAttachment {
	if len(parts) == 0 {
		return nil
	}
	out := make([]ForwardedAttachment, 0, len(parts))
	for _, part := range parts {
		if h, ok := ForwardedAttachmentFromPart(part); ok {
			out = append(out, h)
		}
	}
	return out
}

// ForwardedAttachmentFromPart reads one openable content part.
func ForwardedAttachmentFromPart(part api.MessageContentPart) (ForwardedAttachment, bool) {
	switch part.Origin {
	case api.MessageOriginAttachment:
		h := ForwardedAttachment{
			Filename:  strings.TrimSpace(part.Source),
			MIME:      strings.TrimSpace(part.MediaType),
			Path:      strings.TrimSpace(part.Path),
			SizeBytes: part.SizeBytes,
			Kind:      ForwardedPayload,
		}
		return h, h.Filename != "" && h.MIME != "" && h.Path != "" && h.SizeBytes > 0
	case api.MessageOriginRetrieval:
		switch part.ReferenceKind {
		case api.MessageReferenceKindPathFile, api.MessageReferenceKindPathFolder:
			loc := strings.TrimSpace(part.Path)
			if loc == "" {
				return ForwardedAttachment{}, false
			}
			kind := ForwardedPathFile
			if part.ReferenceKind == api.MessageReferenceKindPathFolder {
				kind = ForwardedPathFolder
			}
			name := path.Base(loc)
			if name == "." || name == "/" || name == "" {
				name = loc
			}
			return ForwardedAttachment{
				Filename:  name,
				MIME:      strings.TrimSpace(part.MediaType),
				Path:      loc,
				SizeBytes: part.SizeBytes,
				Kind:      kind,
				StartLine: part.StartLine,
				EndLine:   part.EndLine,
			}, true
		case api.MessageReferenceKindSearchHit:
		}
	case api.MessageOriginHost, api.MessageOriginUser, api.MessageOriginModel, api.MessageOriginTool,
		api.MessageOriginPeerAgent, api.MessageOriginProject, api.MessageOriginUnknown:
	}
	return ForwardedAttachment{}, false
}
