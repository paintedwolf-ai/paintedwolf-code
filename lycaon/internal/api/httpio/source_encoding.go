package httpio

import (
	"errors"
	"net/http"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/textfile"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func WriteSourceEncodingError(responses *Responder, w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, project.ErrSourceUnsupportedEncoding):
		detected := textfile.Unknown
		var ue *project.SourceUnsupportedEncodingError
		if errors.As(err, &ue) && ue.Detected != "" {
			detected = ue.Detected
		}
		responses.FailDetails(w, wire.ApiErrorCodeUnsupportedEncoding, map[string]any{"detected": detected}, "unsupported text encoding")
	case errors.Is(err, project.ErrSourceEncodingInvalid):
		responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "encoding must be a supported UTF-8 or UTF-16 variant")
	case errors.Is(err, project.ErrSourceDecodeAsInvalid):
		responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "decode_as must be utf-16le or utf-16be for an unsupported file")
	default:
		return false
	}
	return true
}
