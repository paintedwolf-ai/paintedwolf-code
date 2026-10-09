package httpaction

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/tools"
)

// CookieJarFailedCode rejects a named jar that cannot be opened, before any
// request is sent.
const CookieJarFailedCode = "HTTP_REQUEST_COOKIE_JAR_FAILED"

var errCookieReferenceNeedsJar = fmt.Errorf("a {{cookie:…}} reference requires cookie_jar")

// cookieReceipt is what the model learns about a jar: names and counts, never
// a value.
type cookieReceipt struct {
	Jar       string   `json:"jar"`
	Reference string   `json:"reference,omitempty"`
	Sent      int      `json:"sent"`
	Stored    int      `json:"stored"`
	Names     []string `json:"names,omitempty"`
	// Persisted is false when the exchange happened but the jar could not be
	// written back, leaving the next call on the stored version.
	Persisted bool   `json:"persisted"`
	Error     string `json:"error,omitempty"`
}

// facts flattens the receipt for a rejection's structured data, where every
// other field is a scalar or a list.
func (r *cookieReceipt) facts() map[string]any {
	if r == nil {
		return nil
	}
	out := map[string]any{
		"jar": r.Jar, "sent": r.Sent, "stored": r.Stored, "persisted": r.Persisted,
	}
	if r.Reference != "" {
		out["reference"] = r.Reference
	}
	if len(r.Names) > 0 {
		out["names"] = r.Names
	}
	if r.Error != "" {
		out["error"] = r.Error
	}
	return out
}

// openCookieJar loads the named jar for this chat. No jar name means no jar,
// and the request carries only the cookies the caller wrote as headers.
func openCookieJar(ctx context.Context, deps Deps, tctx tools.ToolContext, args map[string]any) (*secretcap.CookieJar, secretcap.CookieJarRequest, error) {
	name, _ := args["cookie_jar"].(string)
	name = strings.TrimSpace(name)
	if _, present := args["cookie_jar"]; !present {
		return nil, secretcap.CookieJarRequest{}, nil
	}
	if name == "" {
		return nil, secretcap.CookieJarRequest{}, invalid(fmt.Errorf("cookie_jar must be a name"))
	}
	if deps.Secrets == nil {
		return nil, secretcap.CookieJarRequest{}, &toolrejection.ToolReject{
			Code: CookieJarFailedCode, Data: map[string]any{"jar": name, "cookie_jar_name": name, "reason": "managed secrets are unavailable"},
		}
	}
	req := secretcap.CookieJarRequest{
		ProjectID: strings.TrimSpace(tctx.ProjectID), ChatSessionID: tctx.ChatSessionID(),
		SessionID: strings.TrimSpace(tctx.SessionID), OperationID: strings.TrimSpace(tctx.ToolCallID), Name: name,
	}
	jar, err := deps.Secrets.OpenCookieJar(ctx, req)
	if err != nil {
		return nil, req, cookieJarOpenReject(name, err)
	}
	return jar, req, nil
}

// saveCookieJar persists cookies even after a failed exchange.
// Persistence errors go on the receipt because the request has already been sent.
func saveCookieJar(ctx context.Context, deps Deps, req secretcap.CookieJarRequest, jar *secretcap.CookieJar) *cookieReceipt {
	if jar == nil {
		return nil
	}
	sent, stored := jar.Store.Counts()
	receipt := &cookieReceipt{
		Jar: jar.Name, Reference: jar.Reference, Sent: sent, Stored: stored,
		Names: jar.Store.Names(), Persisted: true,
	}
	// The server may have set cookies before cancellation or a body timeout.
	// Persist those effects independently, with a bounded shutdown delay.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	meta, err := deps.Secrets.SaveCookieJar(saveCtx, req, jar)
	if err != nil {
		receipt.Persisted, receipt.Error = false, err.Error()
		return receipt
	}
	if meta != nil {
		receipt.Reference = meta.Reference
	}
	return receipt
}

func cookieJarOpenReject(name string, err error) *toolrejection.ToolReject {
	if errors.Is(err, secretcap.ErrInvalidCookieJar) {
		return invalid(fmt.Errorf("cookie_jar name is invalid"))
	}
	return &toolrejection.ToolReject{
		Code: CookieJarFailedCode, Data: map[string]any{"jar": name, "cookie_jar_name": name, "reason": err.Error()},
	}
}
