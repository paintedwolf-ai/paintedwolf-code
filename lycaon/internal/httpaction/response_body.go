package httpaction

import (
	"bytes"
	"mime"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
)

// maxInlineBody bounds what one response puts in front of the model. Anything
// past it lands whole under host data and the result names the file.
const maxInlineBody = 128 << 10

// bodyPlacement is where a response body ended up and what the result may
// therefore claim about it.
type bodyPlacement struct {
	inline   string
	encoding string
	omitted  bool
	// spillPath is host-data relative and readable with read or jq.
	spillPath string
	// spillFailed marks an overflow that could not be landed, so the result
	// reports the body as unavailable.
	spillFailed bool
}

// placeResponseBody decides how a response body reaches the caller. A landed
// or discarded body was never buffered, so there is nothing to place.
func placeResponseBody(resp outboundhttp.Response, tctx tools.ToolContext, landed bool, discarded bool) bodyPlacement {
	switch {
	case landed, discarded, !resp.BodyObserved:
		return bodyPlacement{omitted: true}
	case !textual(resp.ContentType, resp.Body):
		return bodyPlacement{omitted: true, encoding: "binary"}
	case len(resp.Body) <= maxInlineBody:
		return bodyPlacement{inline: string(resp.Body), encoding: "utf-8"}
	}
	rel, ok := landHostData(tctx, resp.Body)
	if !ok {
		return bodyPlacement{omitted: true, encoding: "utf-8", spillFailed: true}
	}
	return bodyPlacement{omitted: true, encoding: "utf-8", spillPath: rel}
}

// landHostData writes an oversized body to the host-data tool-output store,
// which projectpaths resolves for read and the host reclaims on its own.
func landHostData(tctx tools.ToolContext, body []byte) (string, bool) {
	root := strings.TrimSpace(tctx.HostDataDir)
	if root == "" {
		return "", false
	}
	rel := tooloutput.ToolOutputSpillRelPath(string(body))
	store := blobstore.Store{Root: root}
	bound := bytebound.Materialization(tooloutput.DefaultMaxSpillFileBytes)
	if _, err := store.PutAt(rel, bytes.NewReader(body), bound); err != nil {
		return "", false
	}
	return rel, true
}

// textual reports whether a body may be shown as text: a text media type, a
// structured type that is text in practice, or no declared type, and in every
// case only when the bytes are valid UTF-8.
func textual(contentType string, body []byte) bool {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if strings.HasPrefix(mediaType, "text/") {
		return utf8.Valid(body)
	}
	switch mediaType {
	case "application/json", "application/xml", "application/javascript", "application/x-yaml", "image/svg+xml":
		return utf8.Valid(body)
	}
	return mediaType == "" && utf8.Valid(body)
}
