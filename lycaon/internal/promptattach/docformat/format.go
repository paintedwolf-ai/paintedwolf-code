// Package docformat detects which document-family format an attachment is.
package docformat

import (
	"bytes"
	"path"
	"strings"
)

// Format is one of the document-family formats.
type Format string

const (
	PDF  Format = "pdf"
	DOCX Format = "docx"
	XLSX Format = "xlsx"
	PPTX Format = "pptx"
	ODT  Format = "odt"
	ODS  Format = "ods"
	ODP  Format = "odp"
	RTF  Format = "rtf"
)

// Detect identifies a document format from bounded evidence.
func Detect(filename, mime string, raw []byte) (Format, bool) {
	mime = strings.ToLower(strings.TrimSpace(mime))
	ext := strings.ToLower(path.Ext(strings.TrimSpace(filename)))

	if bytes.HasPrefix(raw, []byte("%PDF-")) {
		return PDF, true
	}
	if looksLikeRTF(raw) || mime == "application/rtf" || mime == "text/rtf" || ext == ".rtf" {
		if looksLikeRTF(raw) || ext == ".rtf" || mime == "application/rtf" || mime == "text/rtf" {
			return RTF, true
		}
	}

	switch mime {
	case "application/pdf":
		return PDF, true
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return DOCX, true
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return XLSX, true
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return PPTX, true
	case "application/vnd.oasis.opendocument.text":
		return ODT, true
	case "application/vnd.oasis.opendocument.spreadsheet":
		return ODS, true
	case "application/vnd.oasis.opendocument.presentation":
		return ODP, true
	}

	switch ext {
	case ".pdf":
		return PDF, true
	case ".docx":
		return DOCX, true
	case ".xlsx":
		return XLSX, true
	case ".pptx":
		return PPTX, true
	case ".odt":
		return ODT, true
	case ".ods":
		return ODS, true
	case ".odp":
		return ODP, true
	case ".rtf":
		return RTF, true
	}
	return "", false
}

func looksLikeRTF(raw []byte) bool {
	trim := bytes.TrimSpace(raw)
	return bytes.HasPrefix(trim, []byte(`{\rtf`))
}

// MIME returns the canonical media type for Format.
func MIME(f Format) string {
	switch f {
	case PDF:
		return "application/pdf"
	case DOCX:
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case XLSX:
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case PPTX:
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case ODT:
		return "application/vnd.oasis.opendocument.text"
	case ODS:
		return "application/vnd.oasis.opendocument.spreadsheet"
	case ODP:
		return "application/vnd.oasis.opendocument.presentation"
	case RTF:
		return "application/rtf"
	default:
		return "application/octet-stream"
	}
}

// Ext returns a safe tempfile extension for Format (including the leading dot).
func Ext(f Format) string {
	switch f {
	case PDF:
		return ".pdf"
	case DOCX:
		return ".docx"
	case XLSX:
		return ".xlsx"
	case PPTX:
		return ".pptx"
	case ODT:
		return ".odt"
	case ODS:
		return ".ods"
	case ODP:
		return ".odp"
	case RTF:
		return ".rtf"
	default:
		return ".bin"
	}
}
