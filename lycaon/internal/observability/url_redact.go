package observability

import (
	"net/url"
	"strings"
)

const redactedQueryValue = "REDACTED"

// sensitiveQueryKeyFrags match query names case-insensitively.
var sensitiveQueryKeyFrags = []string{
	"token",
	"key",
	"api_key",
	"access_token",
	"secret",
	"password",
	"passwd",
	"pwd",
	"auth",
	"bearer",
	"credential",
	"signature",
	"sig",
	"otp",
	"session",
	"sid",
}

// RedactURLForLog removes user info and redacts sensitive query values.
func RedactURLForLog(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	changed := false
	if u.User != nil {
		u.User = nil
		changed = true
	}
	q := u.Query()
	for key := range q {
		if queryKeySensitive(key) {
			q.Set(key, redactedQueryValue)
			changed = true
		}
	}
	if !changed {
		return raw
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// RedactQueryForLog redacts sensitive values in a bare query string.
func RedactQueryForLog(rawQuery string) string {
	if strings.TrimSpace(rawQuery) == "" {
		return rawQuery
	}
	q, err := url.ParseQuery(rawQuery)
	changed := false
	for key := range q {
		if queryKeySensitive(key) {
			q.Set(key, redactedQueryValue)
			changed = true
		}
	}
	// Malformed input returns only the parser's sanitized re-encoding.
	if err == nil && !changed {
		return rawQuery
	}
	return q.Encode()
}

func queryKeySensitive(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	if lower == "" {
		return false
	}
	for _, frag := range sensitiveQueryKeyFrags {
		if strings.Contains(lower, frag) {
			return true
		}
	}
	return false
}
