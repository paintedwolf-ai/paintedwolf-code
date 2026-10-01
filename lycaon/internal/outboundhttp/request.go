package outboundhttp

import (
	"net/http"
	"net/netip"
	"net/url"
)

type ResolveMapping struct {
	Host       string
	Port       uint16
	Address    netip.Addr
	TargetPort uint16
}

type preparedRequest struct {
	Request
	Target *url.URL
}

func prepareRequest(in Request, method string) (preparedRequest, error) {
	if !methodAllowed(method) {
		return preparedRequest{}, invalidRequest("unsupported http method %q", method)
	}
	if (method == http.MethodGet || method == http.MethodHead) && len(in.Body) > 0 {
		return preparedRequest{}, invalidRequest("%s requests cannot carry a body", method)
	}
	if err := ValidateHeaders(in.OriginHeaders); err != nil {
		return preparedRequest{}, err
	}
	if err := ValidateHeaders(in.HopHeaders); err != nil {
		return preparedRequest{}, err
	}
	current, err := NormalizeURL(in.URL)
	if err != nil {
		return preparedRequest{}, err
	}
	timeout := in.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if timeout > MaxTimeout {
		timeout = MaxTimeout
	}
	limit := in.MaxBodyBytes
	if limit <= 0 {
		limit = DefaultBodyMax
	}
	redirectMode := in.Redirects
	if redirectMode == "" {
		redirectMode = RedirectNone
	}
	if redirectMode != RedirectNone && redirectMode != RedirectSafe {
		return preparedRequest{}, invalidRequest("redirects must be none or safe")
	}
	redirectLimit := in.RedirectLimit
	if redirectLimit <= 0 {
		redirectLimit = DefaultRedirectLimit
	}
	if redirectLimit > MaxRedirectLimit {
		redirectLimit = MaxRedirectLimit
	}
	in.Method = method
	in.Timeout = timeout
	in.MaxBodyBytes = limit
	in.Redirects = redirectMode
	in.RedirectLimit = redirectLimit
	in.Body = append([]byte(nil), in.Body...)
	in.OriginHeaders = append([]Header(nil), in.OriginHeaders...)
	in.HopHeaders = append([]Header(nil), in.HopHeaders...)
	in.Resolve = append([]ResolveMapping(nil), in.Resolve...)
	return preparedRequest{Request: in, Target: current}, nil
}
