package curl

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// decodeContent undoes the response's Content-Encoding for --compressed, last
// coding first. Decoded bytes stay within the stream ceiling.
func decodeContent(headers []outboundhttp.Header, body []byte, maximum int64) ([]byte, error) {
	var codings []string
	for _, h := range headers {
		if strings.EqualFold(h.Name, "Content-Encoding") {
			for _, coding := range strings.Split(h.Value, ",") {
				if coding = strings.ToLower(strings.TrimSpace(coding)); coding != "" && coding != "identity" {
					codings = append(codings, coding)
				}
			}
		}
	}
	for i := len(codings) - 1; i >= 0; i-- {
		var reader io.Reader
		var err error
		switch codings[i] {
		case "gzip", "x-gzip":
			reader, err = gzip.NewReader(bytes.NewReader(body))
		case "deflate":
			// Servers send zlib-wrapped or raw deflate; curl accepts both.
			reader, err = zlib.NewReader(bytes.NewReader(body))
			if err != nil {
				reader, err = flate.NewReader(bytes.NewReader(body)), nil
			}
		default:
			return nil, &curlFailure{code: 61, message: "Unrecognized content encoding type: " + codings[i], certainty: toolcontract.EffectCompleted}
		}
		if err != nil {
			return nil, &curlFailure{code: 61, message: "Error while processing content unencoding: " + err.Error(), certainty: toolcontract.EffectCompleted, cause: err}
		}
		decoded, err := io.ReadAll(io.LimitReader(reader, maximum+1))
		if err != nil {
			return nil, &curlFailure{code: 61, message: "Error while processing content unencoding: " + err.Error(), certainty: toolcontract.EffectCompleted, cause: err}
		}
		if int64(len(decoded)) > maximum {
			return nil, &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{"limit_id": "stream_bytes", "resource_unit": "bytes", "maximum": maximum, "observed": int64(len(decoded))}}
		}
		body = decoded
	}
	return body, nil
}
