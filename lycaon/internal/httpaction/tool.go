// Package httpaction sends bounded requests to APIs and local services.
package httpaction

import (
	"github.com/lycaon/lycaon/internal/capabilityrequest"
	"github.com/lycaon/lycaon/internal/socketref"

	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"syscall"
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
	// unixSocket is the argument; socket labels the retained destination in receipts.
	unixSocket    string
	socket        string
	socketBinding socketref.Binding
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
		ex, err := exchange(ctx, deps, args, tctx, ExchangeOptions{}, modelResponse)
		var transport *TransportFailure
		if errors.As(err, &transport) {
			return "", transport.Reject
		}
		if err != nil {
			return "", err
		}
		resp := ex.resp
		out := result{
			Status: resp.Status, FinalURL: ex.finalURL, Headers: resp.Headers, Redirects: ex.redirects,
			SentHeaders: ex.sentHeaders,
			DurationMS:  resp.DurationMS, TimingMS: resp.Timing, TLS: resp.TLS,
			Bytes: resp.Bytes, SHA256: resp.SHA256, ContentType: resp.ContentType,
			Redacted: ex.outbound.redacted, ReceiptToken: ex.outbound.receipt, UnixSocket: ex.spec.socket,
			Tokens: ex.tokens, BodyRedacted: ex.bodyRedacted,
		}
		placement := placeResponseBody(ctx, resp, tctx, ex.landed != nil, ex.spec.disposition.discard)
		out.Body, out.BodyEncoding = placement.inline, placement.encoding
		out.BodyOmitted, out.BodySpillPath = placement.omitted, placement.spillPath
		out.BodyUnavailable = placement.spillFailed
		if ex.landed != nil {
			out.ResponsePath, out.Written = ex.landed.Path, true
		}
		out.Cookies = ex.cookies
		reportWebPage(tctx, ex.spec, resp, placement, ex.finalURL)
		if tctx.Effects.Out != nil {
			tctx.Effects.Out.RetrievedFrom = retrievedFrom(ex.spec, resp.FinalURL)
		}
		encoded, err := surveyjson.MarshalIndent(out, "", "  ")
		return string(encoded), err
	})
}

// ExchangeOptions select a typed frontend's response handling. Jar replaces
// a named cookie jar with a frontend-owned one; MaxBodyBytes keeps the whole
// received body in Exchange.Response.Body up to that bound; RedirectLimit
// bounds followed redirects within the owner's maximum; ConnectTimeout bounds
// each hop's connection separately from the exchange timeout.
type ExchangeOptions struct {
	Jar            http.CookieJar
	MaxBodyBytes   int64
	RedirectLimit  int
	ConnectTimeout time.Duration
	// WireHeaders keeps complete headers and any observable cleartext block.
	WireHeaders bool
	// TLS adds request-scoped trust and a client certificate.
	TLS *outboundhttp.ClientTLS
	// Dial replaces the resolve argument: address and connect-to overrides
	// pass the same loopback authority and destination policy as the URL.
	Dial []outboundhttp.ResolveMapping
}

// Exchange is a command frontend's unmodified response. Presentation and
// screening belong to the command's model-delivery boundary. SentBodyBytes
// and SentBodySHA256 identify the request body as it went on the wire.
type Exchange struct {
	Response       outboundhttp.Response
	FinalURL       string
	Redirects      []outboundhttp.Hop
	SentHeaders    []outboundhttp.Header
	SentBodyBytes  int64
	SentBodySHA256 string
}

// Perform runs one request with the same screening, approval, secret,
// cookie, token, loopback and egress handling as the http_request tool. args
// is that tool's declared argument record; response_path and named cookie
// jars stay with the tool. A transport failure is a *TransportFailure.
func Perform(ctx context.Context, deps Deps, args map[string]any, tctx tools.ToolContext, opts ExchangeOptions) (Exchange, error) {
	if _, present := args["response_path"]; present {
		return Exchange{}, invalid(fmt.Errorf("response_path belongs to the http_request tool"))
	}
	if opts.Jar != nil {
		if _, present := args["cookie_jar"]; present {
			return Exchange{}, invalid(fmt.Errorf("a frontend cookie jar replaces cookie_jar"))
		}
	}
	if _, present := args["resolve"]; present && len(opts.Dial) > 0 {
		return Exchange{}, invalid(fmt.Errorf("frontend dial overrides replace resolve"))
	}
	ex, err := exchange(ctx, deps, args, tctx, opts, commandResponse)
	if err != nil {
		return Exchange{}, err
	}
	sum := sha256.Sum256(ex.outbound.body)
	return Exchange{Response: ex.resp, FinalURL: ex.finalURL, Redirects: ex.redirects, SentHeaders: ex.sentHeaders,
		SentBodyBytes: int64(len(ex.outbound.body)), SentBodySHA256: hex.EncodeToString(sum[:])}, nil
}

// TransportFailure is a request the owner started that failed in transport.
// Reject is the owner's typed rejection; Cause keeps the transport error (an
// *outboundhttp.ExchangeError recording requests started and responses
// received) for frontends that report their own status codes.
type TransportFailure struct {
	Reject *toolrejection.ToolReject
	Cause  error
}

func (f *TransportFailure) Error() string   { return f.Reject.Error() }
func (f *TransportFailure) Unwrap() []error { return []error{f.Reject, f.Cause} }

type exchanged struct {
	spec         requestSpec
	outbound     outboundRequest
	resp         outboundhttp.Response
	landed       *inboundwrite.Receipt
	finalURL     string
	redirects    []outboundhttp.Hop
	sentHeaders  []outboundhttp.Header
	tokens       *tokenReceipt
	cookies      *cookieReceipt
	bodyRedacted bool
}

type responseUse uint8

const (
	modelResponse responseUse = iota
	commandResponse
)

// exchange is the owner's complete request lifecycle up to presentation.
func exchange(ctx context.Context, deps Deps, args map[string]any, tctx tools.ToolContext, opts ExchangeOptions, use responseUse) (exchanged, error) {
	ctx = withRequestFiles(ctx)
	spec, err := parseRequestSpec(ctx, deps.Boundary, tctx, args)
	if err != nil {
		return exchanged{}, invalid(err)
	}
	if len(opts.Dial) > 0 {
		spec.resolve = opts.Dial
	}
	capability, reject := capabilityrequest.ParseCapabilityRequest(args)
	if reject != nil {
		return exchanged{}, reject
	}
	allowAddress, reject := loopbackPolicy(capability, tctx)
	if reject != nil {
		return exchanged{}, reject
	}
	if reject := checkLoopbackAuthority(spec, allowAddress); reject != nil {
		return exchanged{}, reject
	}
	jar, jarReq, err := openCookieJar(ctx, deps, tctx, args)
	if err != nil {
		return exchanged{}, err
	}
	tokenJar, tokenJarReq, err := openTokenJar(ctx, deps, tctx, args)
	if err != nil {
		return exchanged{}, err
	}
	if spec.unixSocket != "" {
		grant, reject := tools.ClaimDeclaredSocket(ctx, tctx, spec.unixSocket)
		if reject != nil {
			return exchanged{}, reject
		}
		spec.socket = grant.ResolvedPath
		spec.socketBinding = grant.Binding
	}
	ctx = secretmatch.WithAskAttribution(ctx, secretmatch.AskAttribution{
		SessionID: tctx.Identity.SessionID, RootSessionID: tctx.ChatSessionID(), ProjectID: tctx.Identity.ProjectID,
		ProjectDir: tctx.ActiveRootPath(), ToolCallID: tctx.Identity.ToolCallID,
	})
	screened, err := screenRequest(ctx, deps, spec, args, tctx)
	if err != nil {
		return exchanged{}, err
	}
	cookieSpec := spec
	cookieSpec.target, err = outboundhttp.NormalizeURL(screened.url)
	if err != nil {
		return exchanged{}, invalid(err)
	}
	cookieSpec.hostHeader = screened.hostHeader
	screened.beforeCookieHop, err = prepareCookieDelivery(ctx, deps, cookieSpec, jar)
	if err != nil {
		return exchanged{}, err
	}
	// Stored cookies and references use the same reviewed delivery view.
	outbound, reject := resolveCookieReferences(screened, jar, spec.target)
	if reject != nil {
		return exchanged{}, reject
	}
	// A token is placed at its issuer the same way; anywhere else the
	// screen decides the disclosure before the value touches the wire.
	placements, reject := placedTokens(outbound, tokenJar, spec)
	if reject != nil {
		return exchanged{}, reject
	}
	release, reject := releaseForeignTokens(ctx, deps, spec, tctx, spec.tokenJar, placements)
	if reject != nil {
		return exchanged{}, reject
	}
	outbound, reject = substituteTokens(outbound, placements, release)
	if reject != nil {
		return exchanged{}, reject
	}
	ctx = egressgate.WithAttribution(ctx, confine.EgressCommand{
		SessionID: tctx.Identity.SessionID, RootSessionID: tctx.ChatSessionID(), ProjectID: tctx.Identity.ProjectID,
		ProjectDir: tctx.ActiveRootPath(), ToolCallID: tctx.Identity.ToolCallID, Image: "http_request", ToolName: "http_request",
		ActionID: tctx.EffectActionID(),
	})
	defer confine.ForgetEgressAction(tctx.Identity.SessionID, tctx.EffectActionID())
	resp, landed, bufferedBody, err := send(ctx, deps, tctx, spec, outbound, jar, tokenJar, tokenJarReq, allowAddress, durationArg(args["timeout_ms"]), opts)
	if err != nil {
		if spec.socket != "" && (errors.Is(err, socketref.ErrUnbound) || errors.Is(err, syscall.ECONNREFUSED)) {
			return exchanged{}, capabilityrequest.SocketResolveReject(&confine.SocketResolveError{Code: confine.SocketResolveChanged, Path: spec.unixSocket, Err: err})
		}
		return exchanged{}, &TransportFailure{Reject: sendReject(ctx, deps, jarReq, jar, tokenJarReq, tokenJar, err), Cause: err}
	}
	if bufferedBody != nil {
		resp.Body = bufferedBody
	}
	issuerID, issuerLabel := responseIssuer(spec, resp)
	tokenRec, tokenReject := captureResponseTokens(ctx, deps, tokenJar, tokenJarReq, spec.captureTokens, resp, issuerID, issuerLabel)
	if tokenReject != nil {
		return exchanged{}, tokenReject
	}
	bodyRedacted := false
	finalURL, redirects := resp.FinalURL, resp.Redirects
	if use == modelResponse {
		resp, bodyRedacted = scrubResponse(resp, tokenJar, tctx.Effects.Secrets)
		finalURL, redirects = observedURLs(tctx.Effects.Secrets, resp.FinalURL, resp.Redirects)
	}
	if bufferedBody != nil {
		receipt, err := inboundwrite.Write(ctx, deps.Boundary, tctx, spec.disposition.path, resp.Body, "HTTP_REQUEST_RESPONSE_PATH_DENIED")
		if err != nil {
			return exchanged{}, err
		}
		landed = &receipt
	}
	return exchanged{spec: spec, outbound: outbound, resp: resp, landed: landed, finalURL: finalURL, redirects: redirects,
		sentHeaders: sentHeaders(spec, statedHeaders(tctx, args, screened)), tokens: tokenRec,
		cookies: saveCookieJar(ctx, deps, jarReq, jar), bodyRedacted: bodyRedacted}, nil
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
	allowAddress func(netip.Addr, uint16) bool, timeout time.Duration, opts ExchangeOptions,
) (outboundhttp.Response, *inboundwrite.Receipt, []byte, error) {
	req := outboundhttp.Request{
		Class:  egressclass.AgentHTTPRequest,
		Method: spec.method, URL: outbound.url, OriginHeaders: outbound.headers, Body: outbound.body, Timeout: timeout,
		Redirects: outboundhttp.RedirectMode(spec.redirects), DiscardBody: spec.disposition.discard, AllowAddress: allowAddress,
		UnixSocket:    spec.socket,
		SocketBinding: spec.socketBinding,
		HostHeader:    outbound.hostHeader,
		Resolve:       spec.resolve,
	}
	// A socket request reaches only the reviewed socket; its URL host names
	// no network destination for the egress gate to judge.
	req.BeforeHop = func(hopCtx context.Context, hopURL *url.URL) error {
		if spec.socket == "" {
			if reject := checkHopLoopbackAuthority(hopURL, spec.resolve, allowAddress); reject != nil {
				return reject
			}
			if err := egressgate.AwaitHTTPRequest(hopCtx, hopURL, spec.method); err != nil {
				return err
			}
		}
		if outbound.beforeCookieHop != nil {
			return outbound.beforeCookieHop(hopCtx, hopURL)
		}
		return nil
	}
	if jar != nil {
		req.Jar = jar.Store
	}
	if opts.Jar != nil {
		req.Jar = opts.Jar
	}
	if opts.MaxBodyBytes > 0 {
		req.MaxBodyBytes = opts.MaxBodyBytes
	}
	if opts.RedirectLimit > 0 {
		req.RedirectLimit = opts.RedirectLimit
	}
	req.ConnectTimeout, req.WireHeaders, req.TLS = opts.ConnectTimeout, opts.WireHeaders, opts.TLS
	var landed *inboundwrite.Receipt
	var bufferedResponsePathBytes []byte
	if spec.disposition.path != "" {
		req.MaxBodyBytes = maxResponseFile
		req.StreamBody = func(body io.Reader) error {
			// A body that could echo a held token or a resolved value is
			// buffered so it can be scrubbed before it lands.
			if tokenJar != nil || len(spec.captureTokens) > 0 || tctx.Effects.Secrets.Resolved() {
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
	if err := tctx.Effects.Secrets.HandOff(ctx, spec.outgoing); err != nil {
		return outboundhttp.Response{}, nil, nil, toolrejection.HeldHandOffReject("http_request", err)
	}
	resp, err := outboundhttp.Do(ctx, req)
	return resp, landed, bufferedResponsePathBytes, err
}

// sendReject maps a typed transport failure after received cookies are saved.
func sendReject(
	ctx context.Context, deps Deps, jarReq secretcap.CookieJarRequest, jar *secretcap.CookieJar,
	tokenJarReq secretcap.TokenJarRequest, tokenJar *secretcap.TokenJar, err error,
) *toolrejection.ToolReject {
	cookies := saveCookieJar(ctx, deps, jarReq, jar)
	tokens := heldTokens(tokenJar)
	attach := func(reject *toolrejection.ToolReject) *toolrejection.ToolReject {
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
		return attach(&toolrejection.ToolReject{Code: "HTTP_REQUEST_HOST_DENIED", Data: map[string]any{"host": host}})
	}
	var denied *egress.DestinationDeniedError
	if errors.As(err, &denied) {
		return attach(&toolrejection.ToolReject{Code: "HTTP_REQUEST_HOST_DENIED", Data: map[string]any{"reason": denied.Error()}})
	}
	var reject *toolrejection.ToolReject
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
	return attach(&toolrejection.ToolReject{
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

// hopLoopbackTarget is the loopback address and port a hop dials; ok is
// false when it dials a non-loopback destination.
func hopLoopbackTarget(target *url.URL, resolve []outboundhttp.ResolveMapping) (addr netip.Addr, port uint16, ok bool) {
	if target == nil {
		return netip.Addr{}, 0, false
	}
	port, err := outboundhttp.EffectivePort(target)
	if err != nil {
		return netip.Addr{}, 0, false
	}
	host := target.Hostname()
	if dial, found := outboundhttp.MappedDial(resolve, host, port); found {
		if dial.Address.IsValid() {
			mapped := dial.Address.Unmap()
			return mapped, dial.Port, mapped.IsLoopback()
		}
		host, port = dial.Host, dial.Port
	}
	if !egress.SyntacticLoopback(host) {
		return netip.Addr{}, 0, false
	}
	return netip.MustParseAddr("127.0.0.1"), port, true
}

// firstHop is the loopback address and port the first hop dials; ok is false
// when it dials a unix socket or a non-loopback destination.
func (spec requestSpec) firstHop() (addr netip.Addr, port uint16, ok bool) {
	if spec.socket != "" || spec.unixSocket != "" {
		return netip.Addr{}, 0, false
	}
	return hopLoopbackTarget(spec.target, spec.resolve)
}

func (spec requestSpec) dialsLoopback() bool {
	_, _, ok := spec.firstHop()
	return ok
}

func checkHopLoopbackAuthority(target *url.URL, resolve []outboundhttp.ResolveMapping, allowAddress func(netip.Addr, uint16) bool) *toolrejection.ToolReject {
	addr, port, ok := hopLoopbackTarget(target, resolve)
	if !ok || (allowAddress != nil && allowAddress(addr, port)) {
		return nil
	}
	return &toolrejection.ToolReject{
		Code: isolation.CodeTryLoopbackConnect,
		Data: map[string]any{"port": port, "url": target.String()},
	}
}

func checkLoopbackAuthority(spec requestSpec, allowAddress func(netip.Addr, uint16) bool) *toolrejection.ToolReject {
	if spec.socket != "" || spec.unixSocket != "" {
		return nil
	}
	return checkHopLoopbackAuthority(spec.target, spec.resolve, allowAddress)
}

func loopbackPolicy(request *capabilityrequest.CapabilityRequest, tctx tools.ToolContext) (func(netip.Addr, uint16) bool, *toolrejection.ToolReject) {
	ports := make(map[uint16]bool)
	allLoopback := false
	if tctx.Local.LoopbackConnectGranted {
		if len(tctx.Local.LoopbackConnectPorts) == 0 {
			allLoopback = true
		} else {
			for _, p := range tctx.Local.LoopbackConnectPorts {
				ports[p] = true
			}
		}
	}
	for _, p := range tctx.Local.OwnedLoopbackPorts {
		ports[p] = true
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

func invalid(err error) *toolrejection.ToolReject {
	return &toolrejection.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"reason": err.Error(), "tool": "http_request"}}
}
