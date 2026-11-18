// Package httpaction sends bounded requests to APIs and local services.
package httpaction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/capabilityrequest"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type Deps struct {
	Boundary      *sandbox.Boundary
	SecretMatcher *secretmatch.Matcher
	SecretAsk     secretmatch.AskFunc
	Secrets       SecretMetadata
	Jars          *secretcap.Jars
}

// SecretMetadata supplies value-free identities for jar release review.
type SecretMetadata interface {
	Describe(context.Context, string, string, string) (secretcap.Metadata, error)
}

type requestSpec struct {
	outgoing        func(string) bool
	outgoingHeaders func(string) bool
	outgoingBody    func(string) bool
	method          string
	target          *url.URL
	headers         []outboundhttp.Header
	// headerSources parallels headers.
	headerSources []headerSource
	body          requestBody
	redirects     string
	// unixSocket is the argument; socket labels the retained destination in receipts.
	unixSocket    string
	socket        string
	socketBinding socketref.Binding
	hostHeader    string
	resolve       []outboundhttp.ResolveMapping
	tokenJar      string
	contest       string
	captureTokens []captureTokenRule
}

// ExchangeOptions binds transfer limits, transport overrides and managed output.
type ExchangeOptions struct {
	DecodeManagedBody func([]outboundhttp.Header, []byte, int64) ([]byte, error)
	Output            *JarOutput
	finalHop          func(requestSpec)
	Slots             JarSlots
	Jar               http.CookieJar
	MaxBodyBytes      int64
	RedirectLimit     int
	ConnectTimeout    time.Duration
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
	RedactionReceipts []string
	CookieJarRef      string
	TokenJarRef       string
	Response          outboundhttp.Response
	FinalURL          string
	Redirects         []outboundhttp.Hop
	SentHeaders       []outboundhttp.Header
	SentBodyBytes     int64
	SentBodySHA256    string
	External          bool
	ObservedURLs      []string
	// Listener is the loopback listener the request reached.
	Listener *api.LoopbackListenerFact
	// RemotePage reports a successful remote GET that returned an HTML document.
	RemotePage bool
}

// Perform runs one request curl assembled, with the host's screening, approval,
// secret, cookie, loopback and egress handling. args holds url, method,
// headers, host_header, body_text or form, auth, redirects, timeout_ms and
// unix_socket. A transport failure is a *TransportFailure.
func Perform(ctx context.Context, deps Deps, args map[string]any, tctx tools.ToolContext, opts ExchangeOptions) (Exchange, error) {
	if opts.Jar != nil && args["cookie_jar"] != nil {
		return Exchange{}, invalid(errors.New("choose one cookie store"))
	}
	ex, err := exchange(ctx, deps, args, tctx, opts)
	if err != nil {
		return Exchange{}, opts.Output.diagnostic(err)
	}
	sum := sha256.Sum256(ex.outbound.body)
	extURLs := externalObservedURLs(tctx.Effects.Secrets, ex.spec, ex.finalURL, ex.redirects)
	for i, u := range extURLs {
		extURLs[i] = opts.Output.Text(u)
	}
	return Exchange{
		RedactionReceipts: ex.outbound.redactionReceipts,
		CookieJarRef:      ex.cookieJarRef, TokenJarRef: ex.tokenJarRef,
		Response:       ex.resp,
		FinalURL:       opts.Output.Text(ex.finalURL),
		Redirects:      ex.redirects,
		SentHeaders:    ex.sentHeaders,
		SentBodyBytes:  int64(len(ex.outbound.body)),
		SentBodySHA256: hex.EncodeToString(sum[:]),
		External:       len(extURLs) > 0,
		ObservedURLs:   extURLs,
		Listener:       ex.listener,
		RemotePage:     remotePage(ex.spec, ex.resp),
	}, nil
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
	cookieJarRef string
	tokenJarRef  string
	spec         requestSpec
	outbound     outboundRequest
	resp         outboundhttp.Response
	finalURL     string
	redirects    []outboundhttp.Hop
	sentHeaders  []outboundhttp.Header
	listener     *api.LoopbackListenerFact
}

// exchange is the owner's complete request lifecycle up to presentation.
func exchange(ctx context.Context, deps Deps, args map[string]any, tctx tools.ToolContext, opts ExchangeOptions) (exchanged, error) {
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
	if jar != nil {
		opts.Output.protect(jar.Store.Values()...)
	}
	if tokenJar != nil {
		opts.Output.protect(tokenJar.Values()...)
	}
	if spec.unixSocket != "" {
		grant, reject := tools.ClaimDeclaredSocket(ctx, tctx, spec.unixSocket)
		if reject != nil {
			return exchanged{}, reject
		}
		spec.socket = grant.ResolvedPath
		spec.socketBinding = grant.Binding
	}
	if tctx.Direct.DirectIPRequested {
		if reject := tools.FinalizeDirectIPForSpawn(tctx); reject != nil {
			return exchanged{}, reject
		}
		tools.EmitDirectIPLifecycle(tctx, tools.DirectIPLifecycleStarted)
		defer tools.EmitDirectIPLifecycle(tctx, tools.DirectIPLifecycleCompleted)
	}
	ctx = egressgate.WithReviewedRecipients(ctx)
	deps.SecretAsk = recordReviewedRecipient(deps.SecretAsk)
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
	templates := opts.Slots.templates(screened)
	screened.beforeCookieHop, err = prepareRequestCookieDelivery(ctx, deps, cookieSpec, tctx, jar, opts.Jar, hasCookieReference(templates))
	if err != nil {
		return exchanged{}, err
	}
	outbound, err := placeJarSlots(ctx, deps, tctx, cookieSpec, screened, templates, jar, tokenJar)
	if err != nil {
		return exchanged{}, err
	}
	if reject := reviewClientIdentity(ctx, deps, spec, tctx, opts.TLS); reject != nil {
		return exchanged{}, reject
	}
	// A caller that already attributes the action (native curl names its
	// command line) keeps that attribution and its lifetime.
	if egressgate.From(ctx) == nil {
		ctx = egressgate.WithAttribution(ctx, confine.EgressCommand{
			SessionID: tctx.Identity.SessionID, RootSessionID: tctx.ChatSessionID(), ProjectID: tctx.Identity.ProjectID,
			ProjectDir: tctx.ActiveRootPath(), ToolCallID: tctx.Identity.ToolCallID, Image: "http_request", ToolName: "http_request",
			ActionID: tctx.EffectActionID(),
		})
		defer confine.ForgetEgressAction(tctx.Identity.SessionID, tctx.EffectActionID())
	}
	lastHop := cookieSpec
	opts.finalHop = func(hop requestSpec) {
		lastHop = hop
		if jar != nil {
			opts.Output.protect(jar.Store.Values()...)
		}
	}
	resp, listenerFact, err := send(ctx, deps, tctx, spec, outbound, jar, tokenJar, tokenJarReq, allowAddress, durationArg(args["timeout_ms"]), opts)
	if jar != nil {
		opts.Output.protect(jar.Store.Changes().Values()...)
	}
	if err != nil {
		cookieReceipt := saveCookieJar(ctx, deps, jarReq, jar)
		if spec.socket != "" && (errors.Is(err, socketref.ErrUnbound) || errors.Is(err, syscall.ECONNREFUSED)) {
			return exchanged{}, capabilityrequest.SocketResolveReject(&confine.SocketResolveError{Code: confine.SocketResolveChanged, Path: spec.unixSocket, Err: err})
		}
		var exErr *outboundhttp.ExchangeError
		if reject := toolrejection.AsToolReject(err); reject != nil && (!errors.As(err, &exErr) || (exErr.RequestsStarted == 0 && exErr.ResponsesReceived == 0)) &&
			(reject.Code == toolrejection.OutboundSecretDeniedCode || reject.Code == toolrejection.OutboundSecretScreenFailedCode || toolrejection.IsSecretCapabilityCode(reject.Code)) {
			return exchanged{}, reject
		}
		rejection := sendReject(ctx, err)
		if cookieReceipt != nil {
			if rejection.Data == nil {
				rejection.Data = map[string]any{}
			}
			rejection.Data["cookies"] = cookieReceipt.facts()
		}
		if opts.Output != nil {
			rejection.Data, _ = opts.Output.fact(rejection.Data).(map[string]any)
		}
		return exchanged{}, &TransportFailure{Reject: rejection, Cause: err}
	}
	cookies := saveCookieJar(ctx, deps, jarReq, jar)
	if cookies != nil && !cookies.Persisted {
		return exchanged{}, afterResponse(resp, &toolrejection.ToolReject{Code: CookieJarFailedCode, Data: cookies.facts()})
	}
	if opts.DecodeManagedBody != nil && len(resp.Body) > 0 {
		resp.Body, err = opts.DecodeManagedBody(resp.Headers, resp.Body, opts.MaxBodyBytes)
		if err != nil {
			return exchanged{}, afterResponse(resp, err)
		}
	}
	issuerID, issuerLabel := requestDestination(lastHop)
	tokens, captureErr := captureResponseTokens(ctx, deps, tokenJar, tokenJarReq, spec.captureTokens, resp, issuerID, issuerLabel)
	if captureErr != nil {
		return exchanged{}, afterResponse(resp, captureErr)
	}
	if tokenJar != nil {
		opts.Output.protect(tokenJar.Values()...)
	}
	if tokens != nil && !tokens.Persisted {
		return exchanged{}, afterResponse(resp, &toolrejection.ToolReject{Code: TokenJarFailedCode, Data: tokens.facts()})
	}
	var cookieRef, tokenRef string
	if cookies != nil {
		cookieRef = cookies.Reference
	}
	if tokens != nil {
		tokenRef = tokens.Reference
	}
	finalURL, redirects := resp.FinalURL, resp.Redirects
	return exchanged{cookieJarRef: cookieRef, tokenJarRef: tokenRef, spec: spec, outbound: outbound, resp: resp, finalURL: finalURL, redirects: redirects,
		sentHeaders: sentHeaders(spec, statedHeaders(tctx, args, screened)),
		listener:    listenerFact}, nil
}

func afterResponse(resp outboundhttp.Response, err error) error {
	hops := len(resp.Redirects) + 1
	return &outboundhttp.ExchangeError{RequestsStarted: hops, ResponsesReceived: hops, LastStatus: resp.Status, Err: err}
}

// parseRequestSpec validates declared fields before screening or transport.
func parseRequestSpec(ctx context.Context, boundary *sandbox.Boundary, tctx tools.ToolContext, args map[string]any) (requestSpec, error) {
	method, _ := args["method"].(string)
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		method = "GET"
	}
	rawURL, _ := args["url"].(string)
	if err := referencesOutsideTheirFields(rawURL, args["host_header"]); err != nil {
		return requestSpec{}, err
	}
	target, err := outboundhttp.NormalizeURL(rawURL)
	if err != nil {
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
	redirects, _ := args["redirects"].(string)
	unixSocket, err := parseUnixSocket(args["unix_socket"])
	if err != nil {
		return requestSpec{}, err
	}
	hostHeader, err := parseHostHeader(args["host_header"])
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
	contest, _ := args["unredact"].(string)
	return requestSpec{
		contest: contest, tokenJar: tokenJar, captureTokens: captureTokens,
		method: method, target: target, headers: headers, headerSources: sources, body: body,
		outgoing:        func(path string) bool { return secretmatch.HTTPArgumentConsumed(args, path) },
		outgoingHeaders: func(path string) bool { return secretmatch.HTTPHeaderArgumentConsumed(args, path) },
		outgoingBody:    func(path string) bool { return secretmatch.HTTPBodyArgumentConsumed(args, path) },
		redirects:       redirects,
		unixSocket:      unixSocket, hostHeader: hostHeader,
	}, nil
}

// send revalidates each hop before dispatch.
func send(
	ctx context.Context, deps Deps, tctx tools.ToolContext, spec requestSpec, outbound outboundRequest,
	jar *secretcap.CookieJar, tokenJar *secretcap.TokenJar, tokenJarReq secretcap.TokenJarRequest,
	allowAddress func(netip.Addr, uint16) bool, timeout time.Duration, opts ExchangeOptions,
) (outboundhttp.Response, *api.LoopbackListenerFact, error) {
	var loopbackObs loopbackHopObservation
	var headerDelivery, bodyDelivery *secretcap.PreparedDelivery
	firstHopSeen := false
	req := outboundhttp.Request{
		Class:  egressclass.AgentHTTPRequest,
		Method: spec.method, URL: outbound.url, OriginHeaders: outbound.headers, Body: outbound.body, Timeout: timeout,
		Redirects: outboundhttp.RedirectMode(spec.redirects), AllowAddress: allowAddress,
		UnixSocket:                  spec.socket,
		SocketBinding:               spec.socketBinding,
		HostHeader:                  outbound.hostHeader,
		Resolve:                     spec.resolve,
		OnFirstHopConn:              func(local, remote netip.AddrPort) { loopbackObs.onFirstHopConn(ctx, tctx, local, remote) },
		OnFirstHopHeadersDispatched: func() { headerDelivery.Commit(ctx) },
		OnFirstHopBodyDispatched:    func() { bodyDelivery.Commit(ctx) },
	}
	// A socket request reaches only the reviewed socket; its URL host names
	// no network destination for the egress gate to judge.
	req.BeforeHopRequest = func(hopCtx context.Context, hop outboundhttp.HopRequest) error {
		if spec.socket == "" {
			if reject := checkHopLoopbackAuthority(hop.URL, spec.resolve, allowAddress); reject != nil {
				return reject
			}
		}
		// Cookie review comes first: approving the disclosure is consent to
		// the connection that carries it.
		if outbound.beforeCookieHop != nil {
			reviewHop := hop
			if jar != nil {
				reviewHop = outboundhttp.HopRequest{URL: hop.URL, Method: hop.Method, HostHeader: hop.HostHeader}
			}
			if err := outbound.beforeCookieHop(hopCtx, reviewHop); err != nil {
				return err
			}
		}

		if spec.socket == "" {
			recipientID, _ := requestDestination(spec.forHop(hop))
			if err := admitHopRoute(hopCtx, hop.URL, hop.Method, spec.resolve, recipientID); err != nil {
				return err
			}
		}
		if jar != nil {
			_, err := deps.Jars.RefreshCookieJar(hopCtx, jar)
			if err != nil {
				return cookieJarOpenReject(jar.Name, err)
			}
			// The delivery filter contains only cookies reviewed before the route wait.
			hop.FreezeCookies(jar.Store.Cookies(hop.URL))
			if outbound.validateJarSlots != nil {
				if err := outbound.validateJarSlots(hop); err != nil {
					return err
				}
			}
		}
		if err := checkTokenJarState(hopCtx, deps, tokenJarReq, tokenJar); err != nil {
			return err
		}
		if opts.finalHop != nil {
			opts.finalHop(spec.forHop(hop))
		}
		if !firstHopSeen {
			firstHopSeen = true
			if err := hopCtx.Err(); err != nil {
				return err
			}
			if deps.SecretMatcher != nil {
				if err := deps.SecretMatcher.CheckClassification(hopCtx, outbound.classification); err != nil {
					return screenFaultReject(secretmatch.FaultStageRaise)
				}
			}
			var err error
			headerDelivery, err = tctx.Effects.Secrets.PrepareDelivery(hopCtx, spec.outgoingHeaders)
			if err != nil {
				return toolrejection.HeldHandOffReject("http_request", err)
			}
			bodyDelivery, err = tctx.Effects.Secrets.PrepareDelivery(hopCtx, spec.outgoingBody)
			if err != nil {
				return toolrejection.HeldHandOffReject("http_request", err)
			}
			loopbackObs.beforeFirstHop(hopCtx, tctx, spec)
			return nil
		}
		// A redirect carries the request's material again: a same-origin hop
		// keeps its headers and a 307 or 308 keeps its body. A release revoked
		// while an earlier hop ran is not replayed.
		sameOrigin := secretmatch.HTTPOrigin(hop.URL) == secretmatch.HTTPOrigin(spec.target)
		resendsBody := strings.EqualFold(hop.Method, spec.method)
		if !sameOrigin && !resendsBody {
			return nil
		}
		// What the first hop checked before sending applies again before the
		// material is resent: a classification withdrawn while an earlier hop
		// ran stops the replay.
		if deps.SecretMatcher != nil {
			if err := deps.SecretMatcher.CheckClassification(hopCtx, outbound.classification); err != nil {
				return screenFaultReject(secretmatch.FaultStageRaise)
			}
		}
		if sameOrigin {
			if err := tctx.Effects.Secrets.Precheck(hopCtx, spec.outgoingHeaders); err != nil {
				return toolrejection.HeldHandOffReject("http_request", err)
			}
		}
		if resendsBody {
			if err := tctx.Effects.Secrets.Precheck(hopCtx, spec.outgoingBody); err != nil {
				return toolrejection.HeldHandOffReject("http_request", err)
			}
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
	resp, err := outboundhttp.Do(ctx, req)
	if err != nil {
		return resp, nil, err
	}
	return resp, loopbackObs.settle(ctx, tctx, spec, resp), nil
}

// sendReject maps a typed transport failure.
func sendReject(ctx context.Context, err error) *toolrejection.ToolReject {
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
			if len(exchange.Redirects) > 0 && reject.Data["redirects"] == nil {
				reject.Data["redirects"] = redirectFacts(exchange.Redirects)
			}
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
		data["redirects"] = redirectFacts(redirect.Hops)
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

func redirectFacts(hops []outboundhttp.Hop) []any {
	if len(hops) == 0 {
		return nil
	}
	out := make([]any, 0, len(hops))
	for _, h := range hops {
		item := map[string]any{
			"status":   h.Status,
			"url":      h.URL,
			"location": h.Location,
		}
		if h.Method != "" {
			item["method"] = h.Method
		}
		out = append(out, item)
	}
	return out
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
	if tctx.Local.SocksProxyEnv && (tctx.Direct.DirectIPRequested || (request != nil && request.DirectIP != nil)) {
		return nil, &toolrejection.ToolReject{
			Code: isolation.CodeSocksProxyInvalid,
			Data: map[string]any{"reason": "socks_proxy is incompatible with capability_request.direct_ip"},
		}
	}
	var allowLoopback func(netip.Addr, uint16) bool
	if allLoopback {
		allowLoopback = func(addr netip.Addr, _ uint16) bool {
			return addr.IsLoopback()
		}
	} else if len(ports) > 0 {
		allowLoopback = func(addr netip.Addr, port uint16) bool {
			return addr.IsLoopback() && ports[port]
		}
	}
	directGranted := tctx.Direct.DirectIPAuthorized
	if !directGranted {
		return allowLoopback, nil
	}
	declared := tctx.Direct.DirectIPDeclared
	if len(declared) == 0 && request != nil && request.DirectIP != nil {
		declared = request.DirectIP.DeclaredDestinations
	}
	return func(addr netip.Addr, port uint16) bool {
		if allowLoopback != nil && allowLoopback(addr, port) {
			return true
		}
		return directIPAddressAllowed(declared, addr, port)
	}, nil
}

// directIPAddressAllowed checks whether a non-public unicast destination is
// covered by a granted direct_ip capability and its optional destination list.
func directIPAddressAllowed(declared []string, addr netip.Addr, port uint16) bool {
	addr = addr.Unmap()
	if !egress.IPDirectAllowed(addr) {
		return false
	}
	if permits, narrowed := confine.ParseDirectIPPermits(declared); narrowed {
		portAllowed := false
		for _, p := range permits {
			if p.Protocol == "tcp" && p.Port == port {
				portAllowed = true
				break
			}
		}
		if !portAllowed {
			return false
		}
	}
	if len(declared) == 0 {
		return true
	}
	allConstrained := true
	matched := false
	for _, raw := range declared {
		m, constrained := declaredDirectIPMatch(raw, addr, port)
		if !constrained {
			allConstrained = false
			continue
		}
		if m {
			matched = true
		}
	}
	return !allConstrained || matched
}

func declaredDirectIPMatch(raw string, addr netip.Addr, port uint16) (matched bool, constrained bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return false, false
	}
	if scheme, rest, found := strings.Cut(s, "://"); found {
		switch strings.ToLower(strings.TrimSpace(scheme)) {
		case "tcp", "http", "https":
			s = rest
		case "udp":
			return false, true
		default:
			return false, false
		}
	}
	hostPort, _, _ := strings.Cut(s, "/")
	if host, portText, err := net.SplitHostPort(hostPort); err == nil {
		parsedPort, pErr := strconv.ParseUint(strings.TrimSpace(portText), 10, 16)
		if pErr != nil || parsedPort == 0 {
			return false, true
		}
		portMatches := parsedPort == uint64(port)
		if declaredAddr, aErr := netip.ParseAddr(strings.TrimSuffix(strings.TrimSpace(host), ".")); aErr == nil {
			return portMatches && declaredAddr.Unmap() == addr, true
		}
		return portMatches, true
	}
	trimmed := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(hostPort), "["), "]")
	if declaredAddr, err := netip.ParseAddr(strings.TrimSuffix(trimmed, ".")); err == nil {
		return declaredAddr.Unmap() == addr, true
	}
	return false, false
}

func invalid(err error) *toolrejection.ToolReject {
	return &toolrejection.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"reason": err.Error(), "tool": "http_request"}}
}
