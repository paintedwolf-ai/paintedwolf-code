package pythonevidence

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/pythonadmission"
)

const redactedMarker = "[REDACTED]"

// SecretScreener redacts credentials, tokens, and secrets from text before
// persistence to the execution ledger, retained artifacts, Den projections, or
// model feedback.
type SecretScreener interface {
	ScreenText(text string) (screened string, redactedCount int)
}

// DefaultSecretScreener applies baseline redaction for embedded URL userinfo,
// secret query parameters, bearer/basic authorization tokens, common provider
// token prefixes, PEM private keys, and key=value secret assignments.
type DefaultSecretScreener struct{}

var (
	rePrivateKeyBlock = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	reAuthHeader      = regexp.MustCompile(`(?i)\b(Bearer|Basic|Token)\s+([A-Za-z0-9._~+/=-]{8,})`)
	reKnownToken      = regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{12,}|sk-[A-Za-z0-9_-]{12,}|sk_(?:live|test)_[A-Za-z0-9]{12,}|AKIA[0-9A-Z]{16}|xox[baprs]-[A-Za-z0-9-]{10,}|AIza[0-9A-Za-z_-]{20,})\b`)
	reURLWithCreds    = regexp.MustCompile(`https?://[^\s"'<>]+`)
	reKVSecret        = regexp.MustCompile(`(?i)\b(password|passwd|secret|api_key|apikey|access_token|auth_token|client_secret|aws_secret_access_key|token)\s*([=:])\s*([^\s&"',;]+)`)
)

var secretQueryKeys = map[string]struct{}{
	"api_key":       {},
	"apikey":        {},
	"token":         {},
	"access_token":  {},
	"auth_token":    {},
	"secret":        {},
	"client_secret": {},
	"password":      {},
	"passwd":        {},
	"auth":          {},
	"key":           {},
	"sig":           {},
	"signature":     {},
}

// ScreenText redacts secrets in text and returns the screened text alongside
// the number of redactions applied.
func (DefaultSecretScreener) ScreenText(text string) (string, int) {
	if text == "" {
		return "", 0
	}
	total := 0
	out := text

	out = rePrivateKeyBlock.ReplaceAllStringFunc(out, func(_ string) string {
		total++
		return redactedMarker
	})

	out = reURLWithCreds.ReplaceAllStringFunc(out, func(raw string) string {
		screened, n := screenRawURL(raw)
		total += n
		return screened
	})

	out = reAuthHeader.ReplaceAllStringFunc(out, func(match string) string {
		parts := reAuthHeader.FindStringSubmatch(match)
		if len(parts) < 3 || parts[2] == redactedMarker {
			return match
		}
		total++
		return parts[1] + " " + redactedMarker
	})

	out = reKnownToken.ReplaceAllStringFunc(out, func(_ string) string {
		total++
		return redactedMarker
	})

	out = reKVSecret.ReplaceAllStringFunc(out, func(match string) string {
		parts := reKVSecret.FindStringSubmatch(match)
		if len(parts) < 4 || strings.Contains(parts[3], redactedMarker) {
			return match
		}
		total++
		return parts[1] + parts[2] + redactedMarker
	})

	return out, total
}

func screenRawURL(raw string) (string, int) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw, 0
	}
	redactions := 0
	changed := false

	if u.User != nil {
		redactions++
		changed = true
		u.User = nil
	}

	if u.RawQuery != "" {
		parts := strings.Split(u.RawQuery, "&")
		for i, part := range parts {
			k, v, hasEq := strings.Cut(part, "=")
			if !hasEq || v == "" || v == redactedMarker {
				continue
			}
			if _, isSecret := secretQueryKeys[strings.ToLower(k)]; isSecret {
				parts[i] = k + "=" + redactedMarker
				redactions++
				changed = true
			}
		}
		if changed {
			u.RawQuery = strings.Join(parts, "&")
		}
	}

	if !changed {
		return raw, 0
	}
	res := u.String()
	if redactions > 0 && strings.Contains(raw, "@") && !strings.Contains(res, redactedMarker+"@") {
		if idx := strings.Index(res, "://"); idx >= 0 {
			res = res[:idx+3] + redactedMarker + "@" + res[idx+3:]
		}
	}
	return res, redactions
}

func effectiveScreener(s SecretScreener) SecretScreener {
	if s == nil {
		return DefaultSecretScreener{}
	}
	return s
}

// ScreenText screens text using s (or DefaultSecretScreener if s is nil).
func ScreenText(s SecretScreener, text string) (string, int) {
	return effectiveScreener(s).ScreenText(text)
}

// ScreenURL screens a URL string for embedded credentials and secret query parameters.
func ScreenURL(s SecretScreener, rawURL string) (string, int) {
	if rawURL == "" {
		return "", 0
	}
	screened, n := screenRawURL(rawURL)
	screened2, n2 := effectiveScreener(s).ScreenText(screened)
	return screened2, n + n2
}

// ScreenArgv returns a screened copy of argv and the total redaction count.
func ScreenArgv(s SecretScreener, argv []string) ([]string, int) {
	if len(argv) == 0 {
		return nil, 0
	}
	scr := effectiveScreener(s)
	out := make([]string, len(argv))
	total := 0
	for i, arg := range argv {
		redacted, n := scr.ScreenText(arg)
		out[i] = redacted
		total += n
	}
	return out, total
}

// ScreenHTTPRecord returns a screened copy of an HTTPRecord (URL, FinalURL,
// Redirects, and Error).
func ScreenHTTPRecord(s SecretScreener, rec pythonadmission.HTTPRecord) (pythonadmission.HTTPRecord, int) {
	total := 0
	var n int
	rec.URL, n = ScreenURL(s, rec.URL)
	total += n
	rec.FinalURL, n = ScreenURL(s, rec.FinalURL)
	total += n
	rec.Error, n = ScreenText(s, rec.Error)
	total += n
	if len(rec.Redirects) > 0 {
		hops := make([]pythonadmission.HTTPRedirectHop, len(rec.Redirects))
		for i, hop := range rec.Redirects {
			hop.FromURL, n = ScreenURL(s, hop.FromURL)
			total += n
			hop.ToURL, n = ScreenURL(s, hop.ToURL)
			total += n
			hop.Reason, n = ScreenText(s, hop.Reason)
			total += n
			hops[i] = hop
		}
		rec.Redirects = hops
	}
	return rec, total
}

// ScreenSubprocessRecord returns a screened copy of a SubprocessRecord.
func ScreenSubprocessRecord(s SecretScreener, rec pythonadmission.SubprocessRecord) (pythonadmission.SubprocessRecord, int) {
	total := 0
	var n int
	rec.Argv, n = ScreenArgv(s, rec.Argv)
	total += n
	rec.Reason, n = ScreenText(s, rec.Reason)
	total += n
	return rec, total
}

// ScreenAdmissionRecord returns a screened copy of an AdmissionRecord.
func ScreenAdmissionRecord(s SecretScreener, rec pythonadmission.AdmissionRecord) (pythonadmission.AdmissionRecord, int) {
	total := 0
	var n int
	rec.Reason, n = ScreenText(s, rec.Reason)
	total += n
	if rec.DeclaredFrame != nil {
		copied := *rec.DeclaredFrame
		rec.DeclaredFrame = &copied
	}
	if rec.Outcome != nil {
		copied := *rec.Outcome
		copied.Error, n = ScreenText(s, copied.Error)
		total += n
		rec.Outcome = &copied
	}
	return rec, total
}
