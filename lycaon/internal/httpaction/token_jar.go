package httpaction

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/toolrejection"
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
	// Issuers contains display labels for the issuing services.
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
		"jar": r.Jar, "token_jar_name": r.Jar, "held": r.Held, "captured": r.Captured, "persisted": r.Persisted,
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
	seen := make(map[string]bool, len(items))
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
		if utf8.RuneCountInString(name) > 64 || utf8.RuneCountInString(from) > 128 || seen[name] {
			return nil, fmt.Errorf("capture names must be unique and at most 64 characters, with sources at most 128 characters")
		}
		seen[name] = true
		kind, path, ok := strings.Cut(from, ":")
		if !ok || strings.TrimSpace(path) == "" || (kind != "json" && kind != "header") {
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
	if deps.Jars == nil || deps.Secrets == nil {
		return nil, secretcap.TokenJarRequest{}, &toolrejection.ToolReject{
			Code: TokenJarFailedCode, Data: map[string]any{"jar": name, "token_jar_name": name, "reason": "managed secrets are unavailable"},
		}
	}
	req := secretcap.TokenJarRequest{
		ProjectID: strings.TrimSpace(tctx.Identity.ProjectID), ChatSessionID: tctx.ChatSessionID(),
		SessionID: strings.TrimSpace(tctx.Identity.SessionID), OperationID: "token-jar:" + tctx.EffectActionID(), Name: name,
	}
	jar, err := deps.Jars.OpenTokenJar(ctx, req)
	if err != nil {
		return nil, req, &toolrejection.ToolReject{
			Code: TokenJarFailedCode, Data: map[string]any{"jar": name, "token_jar_name": name, "reason": err.Error()},
		}
	}
	return jar, req, nil
}

func checkTokenJarState(ctx context.Context, deps Deps, req secretcap.TokenJarRequest, jar *secretcap.TokenJar) error {
	if deps.Secrets == nil || jar == nil || jar.Reference == "" || req.ProjectID == "" || req.ChatSessionID == "" {
		return nil
	}
	meta, err := deps.Secrets.Describe(ctx, req.ProjectID, req.ChatSessionID, jar.Reference)
	if err != nil {
		return &toolrejection.ToolReject{
			Code: TokenJarFailedCode, Data: map[string]any{"jar": jar.Name, "token_jar_name": jar.Name, "reason": err.Error()},
		}
	}
	switch meta.State {
	case secretcap.StateRevoked:
		return &toolrejection.ToolReject{
			Code: TokenJarFailedCode, Data: map[string]any{"jar": jar.Name, "token_jar_name": jar.Name, "reason": secretcap.ErrRevoked.Error()},
		}
	case secretcap.StateAgentUseExpired:
		return &toolrejection.ToolReject{
			Code: TokenJarFailedCode, Data: map[string]any{"jar": jar.Name, "token_jar_name": jar.Name, "reason": secretcap.ErrAgentUseExpired.Error()},
		}
	case secretcap.StateUnavailable:
		return &toolrejection.ToolReject{
			Code: TokenJarFailedCode, Data: map[string]any{"jar": jar.Name, "token_jar_name": jar.Name, "reason": secretcap.ErrValueMissing.Error()},
		}
	}
	return nil
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
				if json.Valid(resp.Body) {
					decoder := json.NewDecoder(bytes.NewReader(resp.Body))
					decoder.UseNumber()
					_ = decoder.Decode(&bodyJSON)
				}
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
	case json.Number:
		return v.String(), true
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
	meta, err := deps.Jars.SaveTokenJar(saveCtx, req, jar)
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
	if len(rules) == 0 {
		return heldTokens(jar), nil
	}
	if jar == nil {
		return nil, invalid(fmt.Errorf("capture_tokens requires token_jar"))
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
