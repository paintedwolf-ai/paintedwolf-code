package project

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"net/http"
	"strings"
)

// SourceRawMaxBytes caps GET …/source/raw image payloads.
const SourceRawMaxBytes = 8 * 1024 * 1024

// sourceSniffPrefixBytes bounds MIME and image-magic inspection.
const sourceSniffPrefixBytes = 8192

// Sniffed source MIME kinds the in-app viewer understands.
const (
	SourceMIMEPlain = "text/plain"
	SourceMIMEOctet = "application/octet-stream"
)

// Image MIME types supported by the raw endpoint and viewer.
var sourceImageMIME = map[string]struct{}{
	"image/png":                {},
	"image/jpeg":               {},
	"image/gif":                {},
	"image/webp":               {},
	"image/svg+xml":            {},
	"image/bmp":                {},
	"image/x-icon":             {},
	"image/vnd.microsoft.icon": {},
	"image/avif":               {},
}

// sniffSourceContent classifies a file from its first bytes (not extension).
func sniffSourceContent(prefix []byte) (mime string, image bool) {
	if len(prefix) == 0 {
		return SourceMIMEPlain, false
	}
	if looksLikeSVG(prefix) {
		return "image/svg+xml", true
	}
	detected := http.DetectContentType(prefix)
	if i := strings.IndexByte(detected, ';'); i >= 0 {
		detected = detected[:i]
	}
	detected = strings.ToLower(strings.TrimSpace(detected))
	if detected == "image/bmp" && !looksLikeBMP(prefix) {
		return SourceMIMEPlain, false
	}
	if _, ok := sourceImageMIME[detected]; ok {
		return detected, true
	}
	return detected, false
}

func sourceTextMIME(mime string) string {
	if mime == "" || mime == SourceMIMEOctet {
		return SourceMIMEPlain
	}
	return mime
}

func isSourceImageMIME(mime string) bool {
	mime = strings.ToLower(strings.TrimSpace(mime))
	_, ok := sourceImageMIME[mime]
	return ok
}

func looksLikeSVG(prefix []byte) bool {
	decoder := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(prefix, []byte("\xef\xbb\xbf"))))
	for {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		if element, ok := token.(xml.StartElement); ok {
			return element.Name.Local == "svg" && (element.Name.Space == "" || element.Name.Space == "http://www.w3.org/2000/svg")
		}
		if text, ok := token.(xml.CharData); ok && len(bytes.TrimSpace(text)) > 0 {
			return false
		}
	}
}

func looksLikeBMP(prefix []byte) bool {
	if len(prefix) < 26 || prefix[0] != 'B' || prefix[1] != 'M' {
		return false
	}
	// BITMAPFILEHEADER precedes a DIB header and the pixel payload.
	size := binary.LittleEndian.Uint32(prefix[2:6])
	offset := binary.LittleEndian.Uint32(prefix[10:14])
	dibSize := binary.LittleEndian.Uint32(prefix[14:18])
	return dibSize >= 12 && uint64(offset) >= 14+uint64(dibSize) && offset < size &&
		bytes.Equal(prefix[6:10], []byte{0, 0, 0, 0})
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return SourceMIMEOctet
}
