package httpaction

import (
	"strings"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/tools"
)

// headerSource records which argument produced a request header.
type headerSource uint8

const (
	headerStated headerSource = iota
	headerAuth
	headerHost
)

// sentHeaders echoes the request headers as the call stated them: managed
// secret and cookie references stay reference tokens, and an auth header
// shows only its scheme, so no credential enters the result.
func sentHeaders(spec requestSpec, stated []outboundhttp.Header) []outboundhttp.Header {
	out := make([]outboundhttp.Header, len(spec.headers))
	for i, h := range spec.headers {
		switch spec.headerSources[i] {
		case headerAuth:
			out[i] = outboundhttp.Header{Name: h.Name, Value: AuthScheme(h.Value)}
		case headerStated:
			out[i] = outboundhttp.Header{Name: h.Name}
			if i < len(stated) {
				out[i] = stated[i]
			}
		default:
			out[i] = h
		}
	}
	return out
}

// AuthScheme is the part of a credential header an echo may show: the
// authentication scheme, never the credential.
func AuthScheme(value string) string {
	scheme, _, _ := strings.Cut(value, " ")
	return scheme
}

// CredentialHeader reports a request field whose value is a credential.
func CredentialHeader(name string) bool {
	return strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Proxy-Authorization")
}

// statedHeaders are the headers argument before secret resolution. A redacted
// send reports the redacted values it carried instead.
func statedHeaders(tctx tools.ToolContext, args map[string]any, screened outboundRequest) []outboundhttp.Header {
	if screened.redacted {
		return screened.headers
	}
	canonical := tctx.Effects.CanonicalArgs
	if canonical == nil {
		canonical = args
	}
	headers, err := parseHeaders(canonical["headers"])
	if err != nil {
		return nil
	}
	return headers
}
