// Package httpaction sends bounded requests to APIs and local services.
package httpaction

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/inboundwrite"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

type Deps struct {
	Boundary      *sandbox.Boundary
	SecretMatcher *secretmatch.Matcher
	SecretAsk     secretmatch.AskFunc
	// Named cookie and token jars require the managed secret store.
	Secrets *secretcap.Service
}

type result struct {
	Status       int                     `json:"status"`
	FinalURL     string                  `json:"final_url"`
	Headers      []outboundhttp.Header   `json:"headers,omitempty"`
	SentHeaders  []outboundhttp.Header   `json:"sent_headers,omitempty"`
	Redirects    []outboundhttp.Hop      `json:"redirects,omitempty"`
	DurationMS   int64                   `json:"duration_ms"`
	TimingMS     *outboundhttp.HopTiming `json:"timing_ms,omitempty"`
	TLS          *outboundhttp.TLSInfo   `json:"tls,omitempty"`
	Bytes        int64                   `json:"bytes,omitempty"`
	SHA256       string                  `json:"sha256,omitempty"`
	ContentType  string                  `json:"content_type,omitempty"`
	Body         string                  `json:"body,omitempty"`
	BodyEncoding string                  `json:"body_encoding,omitempty"`
	BodyOmitted  bool                    `json:"body_omitted,omitempty"`
	// BodySpillPath names the full response body covered by SHA256.
	BodySpillPath string `json:"body_spill_path,omitempty"`
	// BodyUnavailable reports a body that was neither inlined nor kept.
	BodyUnavailable bool `json:"body_unavailable,omitempty"`
	// BodyRedacted reports that the response echoed a protected value, so the
	// placed, landed, or spilled bytes carry references where the received
	// bytes covered by SHA256 carried the value.
	BodyRedacted bool `json:"body_redacted,omitempty"`
	// Written confirms ResponsePath contains the bytes covered by SHA256.
	ResponsePath string         `json:"response_path,omitempty"`
	Written      bool           `json:"written,omitempty"`
	Cookies      *cookieReceipt `json:"cookies,omitempty"`
	Tokens       *tokenReceipt  `json:"tokens,omitempty"`
	Redacted     bool           `json:"redacted,omitempty"`
	ReceiptToken string         `json:"redaction_receipt,omitempty"`
	// UnixSocket is the reviewed socket the request reached; the URL host
	// then names no network destination.
	UnixSocket string `json:"unix_socket,omitempty"`
}

type requestSpec struct {
	outgoing func(string) bool
	method   string
	target   *url.URL
	headers  []outboundhttp.Header
	// headerSources parallels headers.
	headerSources []headerSource
	body          requestBody
	redirects     string
	disposition   responseDisposition
	contest       string
	// unixSocket is the argument as stated; socket is the reviewed path dialed.
	unixSocket    string
	socket        string
	hostHeader    string
	resolve       []outboundhttp.ResolveMapping
	tokenJar      string
	captureTokens []captureTokenRule
}

func Register(reg *tools.DefaultRegistry, deps Deps) error {
	if reg == nil || deps.Boundary == nil {
		return fmt.Errorf("registry and sandbox boundary required")
	}
	return reg.Register("http_request", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		ctx = withRequestFiles(ctx)
		spec, err := parseRequestSpec(ctx, deps.Boundary, tctx, args)
		if err != nil {
			return "", invalid(err)
		}
		capability, reject := tools.ParseCapabilityRequest(args)
		if reject != nil {
			return "", reject
		}
		allowAddress, reject := loopbackPolicy(capability, tctx)
		if reject != nil {
			return "", reject
		}
		if reject := checkLoopbackAuthority(spec, allowAddress); reject != nil {
			return "", reject
		}
		jar, jarReq, err := openCookieJar(ctx, deps, tctx, args)
		if err != nil {
			return "", err
		}
		tokenJar, tokenJarReq, err := openTokenJar(ctx, deps, tctx, args)
		if err != nil {
			return "", err
		}
		if spec.unixSocket != "" {
			grant, reject := tools.ClaimDeclaredSocket(ctx, tctx, spec.unixSocket)
			if reject != nil {
				return "", reject
			}
			spec.socket = grant.ResolvedPath
		}
		ctx = secretmatch.WithAskAttribution(ctx, secretmatch.AskAttribution{
			SessionID: tctx.SessionID, RootSessionID: tctx.ChatSessionID(), ProjectID: tctx.ProjectID,
			ProjectDir: tctx.ActiveRootPath(), ToolCallID: tctx.ToolCallID,
		})
		screened, err := screenRequest(ctx, deps, spec, args, tctx)
		if err != nil {
			return "", err
		}
		// Stored cookies are attached after screening and scoped to their origin.
		outbound, reject := resolveCookieReferences(screened, jar, spec.target)
		if reject != nil {
			return "", reject
		}
		// A token is placed at its issuer the same way; anywhere else the
		// screen decides the disclosure before the value touches the wire.
		destinationID, _ := requestDestination(spec)
		placements, reject := placedTokens(outbound, tokenJar, destinationID)
		if reject != nil {
			return "", reject
		}
		release, reject := releaseForeignTokens(ctx, deps, spec, tctx, spec.tokenJar, placements)
		if reject != nil {
			return "", reject
		}
		outbound, reject = substituteTokens(outbound, placements, release)
		if reject != nil {
			return "", reject
		}
		ctx = egressgate.WithAttribution(ctx, confine.EgressCommand{
			SessionID: tctx.SessionID, RootSessionID: tctx.ChatSessionID(), ProjectID: tctx.ProjectID,
			ProjectDir: tctx.ActiveRootPath(), ToolCallID: tctx.ToolCallID, Image: "http_request", ToolName: "http_request",
		})
		defer confine.ForgetEgressAction(tctx.SessionID, tctx.ToolCallID)
		resp, landed, bufferedBody, err := send(ctx, deps, tctx, spec, outbound, jar, tokenJar, tokenJarReq, allowAddress, durationArg(args["timeout_ms"]))
		if err != nil {
			return "", sendReject(ctx, deps, jarReq, jar, tokenJarReq, tokenJar, err)
		}
		if bufferedBody != nil {
			resp.Body = bufferedBody
		}
		issuerID, issuerLabel := responseIssuer(spec, resp)
		tokenRec, tokenReject := captureResponseTokens(ctx, deps, tokenJar, tokenJarReq, spec.captureTokens, resp, issuerID, issuerLabel)
		if tokenReject != nil {
			return "", tokenReject
		}
		// The response is scrubbed before it is placed, landed, or spilled.
		resp, bodyRedacted := scrubResponse(resp, tokenJar, tctx.Secrets)
		if bufferedBody != nil {
			receipt, err := inboundwrite.Write(ctx, deps.Boundary, tctx, spec.disposition.path, resp.Body, "HTTP_REQUEST_RESPONSE_PATH_DENIED")
			if err != nil {
				return "", err
			}
			landed = &receipt
		}
		finalURL, redirects := observedURLs(tctx.Secrets, resp.FinalURL, resp.Redirects)
		out := result{
			Status: resp.Status, FinalURL: finalURL, Headers: resp.Headers, Redirects: redirects,
			SentHeaders: sentHeaders(spec, statedHeaders(tctx, args, screened)),
			DurationMS:  resp.DurationMS, TimingMS: resp.Timing, TLS: resp.TLS,
			Bytes: resp.Bytes, SHA256: resp.SHA256, ContentType: resp.ContentType,
			Redacted: outbound.redacted, ReceiptToken: outbound.receipt, UnixSocket: spec.socket,
			Tokens: tokenRec, BodyRedacted: bodyRedacted,
		}
		placement := placeResponseBody(resp, tctx, landed != nil, spec.disposition.discard)
		out.Body, out.BodyEncoding = placement.inline, placement.encoding
		out.BodyOmitted, out.BodySpillPath = placement.omitted, placement.spillPath
		out.BodyUnavailable = placement.spillFailed
		if landed != nil {
			out.ResponsePath, out.Written = landed.Path, true
		}
		out.Cookies = saveCookieJar(ctx, deps, jarReq, jar)
		reportWebPage(tctx, spec, resp, placement, finalURL)
		if tctx.Out != nil {
			tctx.Out.RetrievedFrom = retrievedFrom(spec, resp.FinalURL)
		}
		encoded, err := surveyjson.MarshalIndent(out, "", "  ")
		return string(encoded), err
	})
}

// parseRequestSpec validates declared fields before screening or transport.
func parseRequestSpec(ctx context.Context, boundary *sandbox.Boundary, tctx tools.ToolContext, args map[string]any) (requestSpec, error) {
	method, _ := args["method"].(string)
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		method = "GET"
	}
	rawURL, _ := args["url"].(string)
	target, err := outboundhttp.NormalizeURL(rawURL)
	if err != nil {
		return requestSpec{}, err
	}
	if err := applyQuery(target, args["query"]); err != nil {
		return requestSpec{}, err
	}
	headers, err := parseHeaders(args["headers"])
	if err != nil {
		return requestSpec{}, err
	}
	sources := make([]headerSource, len(headers), len(headers)+2)
	if auth, err := authHeader(args["auth"], headers); err != nil {
		return requestSpec{}, err
	} else if auth != nil {
		headers = append(headers, *auth)
		sources = append(sources, headerAuth)
	}
	body, err := assembleBody(ctx, boundary, tctx, args)
	if err != nil {
		return requestSpec{}, err
	}
	if hasRequestBody(args) && (method == "GET" || method == "HEAD") {
		return requestSpec{}, fmt.Errorf("%s requests cannot carry a body; set an explicit body-capable method", method)
	}
	if body.contentType != "" && !hasHeader(headers, "content-type") {
		headers = append(headers, outboundhttp.Header{Name: "Content-Type", Value: body.contentType})
		sources = append(sources, headerHost)
	}
	if err := outboundhttp.ValidateHeaders(headers); err != nil {
		return requestSpec{}, err
	}
	disposition, err := parseResponseDisposition(args)
	if err != nil {
		return requestSpec{}, err
	}
	redirects, _ := args["redirects"].(string)
	contest, _ := args["unredact"].(string)
	unixSocket, err := parseUnixSocket(args["unix_socket"])
	if err != nil {
		return requestSpec{}, err
	}
	hostHeader, err := parseHostHeader(args["host_header"])
	if err != nil {
		return requestSpec{}, err
	}
	resolve, err := parseResolve(args["resolve"])
	if err != nil {
		return requestSpec{}, err
	}
	captureTokens, err := parseCaptureTokens(args["capture_tokens"])
	if err != nil {
		return requestSpec{}, err
	}
	tokenJar, _ := args["token_jar"].(string)
	tokenJar = strings.TrimSpace(tokenJar)
	if len(captureTokens) > 0 && tokenJar == "" {
		return requestSpec{}, fmt.Errorf("capture_tokens requires token_jar")
	}
	return requestSpec{
		method: method, target: target, headers: headers, headerSources: sources, body: body,
		outgoing:  func(path string) bool { return secretmatch.HTTPArgumentConsumed(args, path) },
		redirects: redirects, disposition: disposition, contest: contest,
		unixSocket: unixSocket, hostHeader: hostHeader, resolve: resolve,
		tokenJar: tokenJar, captureTokens: captureTokens,
	}, nil
}

// response_path bodies land through the write door and return a file receipt;
// one that may need a scrub is buffered first.
func send(
	ctx context.Context, deps Deps, tctx tools.ToolContext, spec requestSpec, outbound outboundRequest,
	jar *secretcap.CookieJar, tokenJar *secretcap.TokenJar, tokenJarReq secretcap.TokenJarRequest,
	allowAddress func(netip.Addr, uint16) bool, timeout time.Duration,
) (outboundhttp.Response, *inboundwrite.Receipt, []byte, error) {
	req := outboundhttp.Request{
		Class:  egressclass.AgentHTTPRequest,
		Method: spec.method, URL: outbound.url, OriginHeaders: outbound.headers, Body: outbound.body, Timeout: timeout,
		Redirects: outboundhttp.RedirectMode(spec.redirects), DiscardBody: spec.disposition.discard, AllowAddress: allowAddress,
		UnixSocket: spec.socket,
		HostHeader: spec.hostHeader,
		Resolve:    spec.resolve,
	}
	// A socket request reaches only the reviewed socket; its URL host names
	// no network destination for the egress gate to judge.
	if spec.socket == "" {
		req.BeforeHop = func(hopCtx context.Context, hopURL *url.URL) error {
			return egressgate.AwaitHTTPRequest(hopCtx, hopURL, spec.method)
		}
	}
	if jar != nil {
		req.Jar = jar.Store
	}
	var landed *inboundwrite.Receipt
	var bufferedResponsePathBytes []byte
	if spec.disposition.path != "" {
		req.MaxBodyBytes = maxResponseFile
		req.StreamBody = func(body io.Reader) error {
			// A body that could echo a held token or a resolved value is
			// buffered so it can be scrubbed before it lands.
			if tokenJar != nil || len(spec.captureTokens) > 0 || tctx.Secrets.Resolved() {
				data, err := io.ReadAll(io.LimitReader(body, maxResponseFile+1))
				if err != nil {
					return err
				}
				if int64(len(data)) > maxResponseFile {
					return &outboundhttp.BodyTooLargeError{Limit: maxResponseFile}
				}
				bufferedResponsePathBytes = data
				return nil
			}
			receipt, err := inboundwrite.WriteFrom(ctx, deps.Boundary, tctx, spec.disposition.path, body, "HTTP_REQUEST_RESPONSE_PATH_DENIED")
			if err != nil {
				return err
			}
			landed = &receipt
			return nil
		}
	}
	if err := tctx.Secrets.HandOff(ctx, spec.outgoing); err != nil {
		return outboundhttp.Response{}, nil, nil, tools.HeldHandOffReject("http_request", err)
	}
	resp, err := outboundhttp.Do(ctx, req)
	return resp, landed, bufferedResponsePathBytes, err
}

// sendReject maps a typed transport failure after received cookies are saved.
func sendReject(
	ctx context.Context, deps Deps, jarReq secretcap.CookieJarRequest, jar *secretcap.CookieJar,
	tokenJarReq secretcap.TokenJarRequest, tokenJar *secretcap.TokenJar, err error,
) *tools.ToolReject {
	cookies := saveCookieJar(ctx, deps, jarReq, jar)
	tokens := heldTokens(tokenJar)
	attach := func(reject *tools.ToolReject) *tools.ToolReject {
		var exchange *outboundhttp.ExchangeError
		if errors.As(err, &exchange) {
			if reject.Data == nil {
				reject.Data = map[string]any{}
			}
			reject.Data["http_method"] = exchange.Method
			reject.Data["http_requests_started"] = exchange.RequestsStarted
			reject.Data["http_responses_received"] = exchange.ResponsesReceived
			reject.Data["http_last_status"] = exchange.LastStatus
		}
		if cookies != nil {
			if reject.Data == nil {
				reject.Data = map[string]any{}
			}
			reject.Data["cookies"] = cookies.facts()
		}
		if tokens != nil {
			if reject.Data == nil {
				reject.Data = map[string]any{}
			}
			reject.Data["tokens"] = tokens.facts()
		}
		var tlsFault *outboundhttp.TLSFault
		if errors.As(err, &tlsFault) {
			if reject.Data == nil {
				reject.Data = map[string]any{}
			}
			reject.Data["tls_fault"] = tlsFault.Type
			detail := tlsFault.Detail
			if detail == "" {
				detail = tlsFault.Reason
			}
			reject.Data["tls_detail"] = detail
			if tlsFault.Host != "" {
				reject.Data["host"] = tlsFault.Host
			}
		}
		return reject
	}
	if host, denied := egressgate.Denied(ctx); denied {
		return attach(&tools.ToolReject{Code: "HTTP_REQUEST_HOST_DENIED", Data: map[string]any{"host": host}})
	}
	var denied *egress.DestinationDeniedError
	if errors.As(err, &denied) {
		return attach(&tools.ToolReject{Code: "HTTP_REQUEST_HOST_DENIED", Data: map[string]any{"reason": denied.Error()}})
	}
	var reject *tools.ToolReject
	if errors.As(err, &reject) {
		return attach(reject)
	}
	retryable := !outboundhttp.Permanent(err)
	data := map[string]any{"reason": err.Error(), "retryable": retryable}
	var redirect *outboundhttp.RedirectError
	if errors.As(err, &redirect) && len(redirect.Hops) > 0 {
		data["redirects"] = redirect.Hops
	}
	var oversized *outboundhttp.BodyTooLargeError
	if errors.As(err, &oversized) {
		data["limit_bytes"] = oversized.Limit
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		data["deadline"] = "timeout_ms"
	}
	return attach(&tools.ToolReject{
		Code: "HTTP_REQUEST_FAILED", FailureClass: api.FailureClassOwnerError,
		Retryable: retryable, Data: data,
	})
}

// retrievedFrom labels where response content came from.
func retrievedFrom(spec requestSpec, finalURL string) string {
	if spec.socket != "" {
		return "unix:" + spec.socket
	}
	if target, err := outboundhttp.NormalizeURL(finalURL); err == nil {
		return target.Hostname()
	}
	return ""
}

// firstHop is the loopback address and port the first hop dials; ok is false
// when it dials a unix socket or a non-loopback destination.
func (spec requestSpec) firstHop() (addr netip.Addr, port uint16, ok bool) {
	if spec.socket != "" || spec.unixSocket != "" || spec.target == nil {
		return netip.Addr{}, 0, false
	}
	port, err := outboundhttp.EffectivePort(spec.target)
	if err != nil {
		return netip.Addr{}, 0, false
	}
	if mapped, target, found := outboundhttp.MappedDial(spec.resolve, spec.target.Hostname(), port); found {
		mapped = mapped.Unmap()
		return mapped, target, mapped.IsLoopback()
	}
	if !egress.SyntacticLoopback(spec.target.Hostname()) {
		return netip.Addr{}, 0, false
	}
	return netip.MustParseAddr("127.0.0.1"), port, true
}

func (spec requestSpec) dialsLoopback() bool {
	_, _, ok := spec.firstHop()
	return ok
}

func checkLoopbackAuthority(spec requestSpec, allowAddress func(netip.Addr, uint16) bool) *tools.ToolReject {
	addr, port, ok := spec.firstHop()
	if !ok || (allowAddress != nil && allowAddress(addr, port)) {
		return nil
	}
	return &tools.ToolReject{
		Code: isolation.CodeTryLoopbackConnect,
		Data: map[string]any{"port": port, "url": spec.target.String()},
	}
}

func loopbackPolicy(request *tools.CapabilityRequest, tctx tools.ToolContext) (func(netip.Addr, uint16) bool, *tools.ToolReject) {
	ports := make(map[uint16]bool)
	allLoopback := false
	if tctx.LoopbackConnectGranted {
		if len(tctx.LoopbackConnectPorts) == 0 {
			allLoopback = true
		} else {
			for _, p := range tctx.LoopbackConnectPorts {
				ports[p] = true
			}
		}
	}
	if request != nil && request.LoopbackConnect != nil {
		if len(request.LoopbackConnect.Ports) == 0 && len(ports) == 0 && !allLoopback {
			return nil, invalid(fmt.Errorf("capability_request.loopback_connect requires exact ports"))
		}
		for _, port := range request.LoopbackConnect.Ports {
			ports[port] = true
		}
	}
	if allLoopback {
		return func(addr netip.Addr, port uint16) bool {
			return addr.IsLoopback()
		}, nil
	}
	if len(ports) == 0 {
		return nil, nil
	}
	return func(addr netip.Addr, port uint16) bool {
		return addr.IsLoopback() && ports[port]
	}, nil
}

func invalid(err error) *tools.ToolReject {
	return &tools.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"reason": err.Error(), "tool": "http_request"}}
}

