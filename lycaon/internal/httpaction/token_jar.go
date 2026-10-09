package httpaction

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/tools"
)

const (
	TokenNotHeldCode       = "HTTP_REQUEST_TOKEN_NOT_HELD"
	TokenJarFailedCode     = "HTTP_REQUEST_TOKEN_JAR_FAILED"
	TokenCaptureFailedCode = "HTTP_REQUEST_TOKEN_CAPTURE_FAILED"
)

var (
	errTokenReferenceNeedsJar = fmt.Errorf("a {{token:…}} reference requires token_jar")
	tokenReference            = regexp.MustCompile(`\{\{token:([^{}:]+)\}\}`)
)

// tokenReceipt reports a jar without its values.
type tokenReceipt struct {
	Jar       string   `json:"jar"`
	Reference string   `json:"reference,omitempty"`
	Held      int      `json:"held"`
	Captured  int      `json:"captured"`
	Names     []string `json:"names,omitempty"`
	// Issuers maps each held token to the service that issued it, which is
	// where a reference to it is placed without review.
	Issuers   map[string]string `json:"issuers,omitempty"`
	Persisted bool              `json:"persisted"`
	Error     string            `json:"error,omitempty"`
}

// heldTokens describes the jar's current contents as a receipt.
func heldTokens(jar *secretcap.TokenJar) *tokenReceipt {
	if jar == nil {
		return nil
	}
	receipt := &tokenReceipt{Jar: jar.Name, Reference: jar.Reference, Held: len(jar.Tokens), Persisted: true}
	if len(jar.Tokens) == 0 {
		return receipt
	}
	receipt.Names = make([]string, 0, len(jar.Tokens))
	receipt.Issuers = make(map[string]string, len(jar.Tokens))
	for name, token := range jar.Tokens {
		receipt.Names = append(receipt.Names, name)
		receipt.Issuers[name] = token.OriginLabel
	}
	sort.Strings(receipt.Names)
	return receipt
}

func (r *tokenReceipt) facts() map[string]any {
	if r == nil {
		return nil
	}
	out := map[string]any{
		"jar": r.Jar, "held": r.Held, "captured": r.Captured, "persisted": r.Persisted,
	}
	if r.Reference != "" {
		out["reference"] = r.Reference
	}
	if len(r.Names) > 0 {
		out["names"] = r.Names
	}
	if len(r.Issuers) > 0 {
		out["issuers"] = r.Issuers
	}
	if r.Error != "" {
		out["error"] = r.Error
	}
	return out
}

type captureTokenRule struct {
	Name string
	From string
}

func parseCaptureTokens(raw any) ([]captureTokenRule, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("capture_tokens must be an array")
	}
	if len(items) > 16 {
		return nil, fmt.Errorf("capture_tokens accepts at most 16 rules")
	}
	out := make([]captureTokenRule, 0, len(items))
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("each capture_tokens rule must be an object")
		}
		name, _ := obj["name"].(string)
		from, _ := obj["from"].(string)
		name = strings.TrimSpace(name)
		from = strings.TrimSpace(from)
		if name == "" || from == "" {
			return nil, fmt.Errorf("each capture_tokens rule requires name and from")
		}
		if !strings.HasPrefix(from, "json:") && !strings.HasPrefix(from, "header:") {
			return nil, fmt.Errorf("capture_tokens from must begin with 'json:' or 'header:'")
		}
		out = append(out, captureTokenRule{Name: name, From: from})
	}
	return out, nil
}

func openTokenJar(ctx context.Context, deps Deps, tctx tools.ToolContext, args map[string]any) (*secretcap.TokenJar, secretcap.TokenJarRequest, error) {
	name, _ := args["token_jar"].(string)
	name = strings.TrimSpace(name)
	if _, present := args["token_jar"]; !present {
		return nil, secretcap.TokenJarRequest{}, nil
	}
	if name == "" {
		return nil, secretcap.TokenJarRequest{}, invalid(fmt.Errorf("token_jar must be a name"))
	}
	if deps.Secrets == nil {
		return nil, secretcap.TokenJarRequest{}, &toolrejection.ToolReject{
			Code: TokenJarFailedCode, Data: map[string]any{"jar": name, "token_jar_name": name, "reason": "managed secrets are unavailable"},
		}
	}
	req := secretcap.TokenJarRequest{
		ProjectID: strings.TrimSpace(tctx.ProjectID), ChatSessionID: tctx.ChatSessionID(),
		SessionID: strings.TrimSpace(tctx.SessionID), OperationID: strings.TrimSpace(tctx.ToolCallID), Name: name,
	}
	jar, err := deps.Secrets.OpenTokenJar(ctx, req)
	if err != nil {
		return nil, req, &toolrejection.ToolReject{
			Code: TokenJarFailedCode, Data: map[string]any{"jar": name, "token_jar_name": name, "reason": err.Error()},
		}
	}
	return jar, req, nil
}

// tokenPlacement is one {{token:name}} reference in the request and the
// token it names.
type tokenPlacement struct {
	name  string
	token secretcap.Token
	// foreign marks a token issued by a service other than the request's
	// destination, so placing it discloses the credential to a new recipient.
	foreign bool
}

// referencedTokenNames lists the names the request references, once each.
func referencedTokenNames(req outboundRequest) []string {
	var names []string
	collect := func(in string) {
		for _, match := range tokenReference.FindAllStringSubmatch(in, -1) {
			names = append(names, strings.TrimSpace(match[1]))
		}
	}
	collect(string(req.body))
	for _, header := range req.headers {
		collect(header.Value)
	}
	return sortedUnique(names)
}

// placedTokens resolves every reference against the jar and marks the ones
// whose issuer is not this request's destination. An unheld name is refused
// rather than sent literally.
func placedTokens(req outboundRequest, jar *secretcap.TokenJar, destinationID string) ([]tokenPlacement, *toolrejection.ToolReject) {
	names := referencedTokenNames(req)
	if len(names) == 0 {
		return nil, nil
	}
	if jar == nil {
		return nil, invalid(errTokenReferenceNeedsJar)
	}
	var missing []string
	placements := make([]tokenPlacement, 0, len(names))
	for _, name := range names {
		token, ok := jar.Lookup(name)
		if !ok {
			missing = append(missing, name)
			continue
		}
		placements = append(placements, tokenPlacement{name: name, token: token, foreign: token.Origin != destinationID})
	}
	if len(missing) > 0 {
		return nil, &toolrejection.ToolReject{
			Code: TokenNotHeldCode,
			Data: map[string]any{"jar": jar.Name, "missing": missing, "held": jar.Names()},
		}
	}
	return placements, nil
}

// substituteTokens writes each placed token onto the wire. A foreign token
// the screen released redacted is written as the marker instead, and the
// request then carries the redaction receipt.
func substituteTokens(req outboundRequest, placements []tokenPlacement, release secretmatch.Resolution) (outboundRequest, *toolrejection.ToolReject) {
	if len(placements) == 0 {
		return req, nil
	}
	spelling := make(map[string]string, len(placements))
	redacted := false
	for _, placement := range placements {
		spelling[placement.name] = placement.token.Value
		if placement.foreign && release.Decision == secretmatch.SendRedacted {
			spelling[placement.name] = secretmatch.RedactedMarker
			redacted = true
		}
	}
	substitute := func(in string) string {
		return tokenReference.ReplaceAllStringFunc(in, func(match string) string {
			return spelling[strings.TrimSpace(tokenReference.FindStringSubmatch(match)[1])]
		})
	}
	out := outboundRequest{
		url:      req.url,
		headers:  make([]outboundhttp.Header, len(req.headers)),
		body:     []byte(substitute(string(req.body))),
		redacted: req.redacted || redacted,
		receipt:  req.receipt,
	}
	if redacted && out.receipt == "" {
		out.receipt = release.ReceiptToken
	}
	for i, header := range req.headers {
		out.headers[i] = outboundhttp.Header{Name: header.Name, Value: substitute(header.Value)}
	}
	if err := outboundhttp.ValidateHeaders(out.headers); err != nil {
		return outboundRequest{}, invalid(err)
	}
	return out, nil
}

// responseIssuer identifies the service whose response carried a captured
// token: the reviewed socket, or the origin the exchange ended at.
func responseIssuer(spec requestSpec, resp outboundhttp.Response) (id, label string) {
	if spec.socket != "" {
		return requestDestination(spec)
	}
	if final, err := url.Parse(strings.TrimSpace(resp.FinalURL)); err == nil && final.Hostname() != "" {
		return secretmatch.HTTPDestination(final)
	}
	return secretmatch.HTTPDestination(spec.target)
}

func extractTokens(resp outboundhttp.Response, rules []captureTokenRule) (map[string]string, []string) {
	if len(rules) == 0 {
		return nil, nil
	}
	captured := make(map[string]string)
	var missing []string

	var bodyJSON any
	bodyParsed := false

	for _, rule := range rules {
		if strings.HasPrefix(rule.From, "header:") {
			headerName := strings.TrimSpace(strings.TrimPrefix(rule.From, "header:"))
			found := false
			for _, h := range resp.Headers {
				if strings.EqualFold(h.Name, headerName) {
					val := strings.TrimSpace(h.Value)
					if strings.HasPrefix(strings.ToLower(val), "bearer ") {
						val = strings.TrimSpace(val[7:])
					}
					captured[rule.Name] = val
					found = true
					break
				}
			}
			if !found {
				missing = append(missing, rule.Name)
			}
			continue
		}

		if strings.HasPrefix(rule.From, "json:") {
			jsonPath := strings.TrimSpace(strings.TrimPrefix(rule.From, "json:"))
			if !bodyParsed {
				bodyParsed = true
				_ = json.Unmarshal(resp.Body, &bodyJSON)
			}
			if bodyJSON == nil {
				missing = append(missing, rule.Name)
				continue
			}
			val, ok := lookupJSONPath(bodyJSON, jsonPath)
			if ok && val != "" {
				captured[rule.Name] = val
			} else {
				missing = append(missing, rule.Name)
			}
		}
	}
	return captured, missing
}

func lookupJSONPath(root any, p string) (string, bool) {
	p = strings.TrimPrefix(p, "/")
	parts := strings.FieldsFunc(p, func(r rune) bool { return r == '.' || r == '/' })
	curr := root
	for _, part := range parts {
		switch obj := curr.(type) {
		case map[string]any:
			var ok bool
			curr, ok = obj[part]
			if !ok {
				return "", false
			}
		default:
			return "", false
		}
	}
	switch v := curr.(type) {
	case string:
		return v, true
	case float64:
		return fmt.Sprintf("%v", v), true
	case bool:
		return fmt.Sprintf("%v", v), true
	default:
		return "", false
	}
}

func saveTokenJar(ctx context.Context, deps Deps, req secretcap.TokenJarRequest, jar *secretcap.TokenJar, capturedCount int) *tokenReceipt {
	receipt := heldTokens(jar)
	if receipt == nil {
		return nil
	}
	receipt.Captured = capturedCount
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	meta, err := deps.Secrets.SaveTokenJar(saveCtx, req, jar)
	if err != nil {
		receipt.Persisted, receipt.Error = false, err.Error()
		return receipt
	}
	if meta != nil {
		receipt.Reference = meta.Reference
	}
	return receipt
}

// captureResponseTokens stores what the rules extract from the raw response,
// each bound to the service that issued it, and saves the jar.
func captureResponseTokens(
	ctx context.Context,
	deps Deps,
	jar *secretcap.TokenJar,
	req secretcap.TokenJarRequest,
	rules []captureTokenRule,
	resp outboundhttp.Response,
	issuerID, issuerLabel string,
) (*tokenReceipt, *toolrejection.ToolReject) {
	if jar == nil && len(rules) == 0 {
		return nil, nil
	}
	if jar == nil && len(rules) > 0 {
		return nil, invalid(fmt.Errorf("capture_tokens requires token_jar"))
	}
	if len(rules) == 0 {
		receipt := saveTokenJar(ctx, deps, req, jar, 0)
		return receipt, nil
	}
	captured, missing := extractTokens(resp, rules)
	if len(missing) > 0 {
		return nil, &toolrejection.ToolReject{
			Code: TokenCaptureFailedCode,
			Data: map[string]any{"jar": jar.Name, "missing": missing},
		}
	}
	for name, value := range captured {
		jar.Set(name, secretcap.Token{Value: value, Origin: issuerID, OriginLabel: issuerLabel})
	}
	receipt := saveTokenJar(ctx, deps, req, jar, len(captured))
	return receipt, nil
}
