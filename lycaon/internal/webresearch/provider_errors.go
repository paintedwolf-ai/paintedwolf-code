package webresearch

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ProviderErrorState classifies a provider's non-2xx response.
type ProviderErrorState string

const (
	ErrorCredentialRejected ProviderErrorState = "credential_rejected"
	ErrorQuotaExhausted     ProviderErrorState = "quota_exhausted"
	ErrorRateLimited        ProviderErrorState = "rate_limited"
	ErrorRequestRejected    ProviderErrorState = "request_rejected"
	ErrorUnavailable        ProviderErrorState = "unavailable"
)

func (s ProviderErrorState) valid() bool {
	switch s {
	case ErrorCredentialRejected, ErrorQuotaExhausted, ErrorRateLimited, ErrorRequestRejected, ErrorUnavailable:
		return true
	default:
		return false
	}
}

// ErrorSpec maps a provider's error responses where they differ from the
// default status table.
type ErrorSpec struct {
	// CodePaths are dot-separated JSON paths to a machine error code; numeric
	// segments index arrays.
	CodePaths []string                      `yaml:"code_paths"`
	Codes     map[string]ProviderErrorState `yaml:"codes"`
	Statuses  map[int]ProviderErrorState    `yaml:"statuses"`
}

// ProviderError is a classified non-2xx provider response. Code is a machine
// token from the body or http_<status>, never response prose.
type ProviderError struct {
	State ProviderErrorState
	Code  string
}

const providerErrorBodyLimit = 8 << 10

var providerErrorCode = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

func classifyProviderError(spec *ErrorSpec, status int, body []byte, credentialSent bool) ProviderError {
	out := ProviderError{Code: "http_" + strconv.Itoa(status)}
	if spec != nil {
		if code, state := spec.matchCode(body); code != "" {
			out.Code, out.State = code, state
		}
		if out.State == "" {
			out.State = spec.Statuses[status]
		}
	}
	if out.State == "" {
		out.State = defaultErrorState(status)
	}
	if out.State == ErrorCredentialRejected && !credentialSent {
		out.State = ErrorRequestRejected
	}
	return out
}

// matchCode returns the first well-formed code on CodePaths, preferring one
// listed in Codes.
func (spec *ErrorSpec) matchCode(body []byte) (string, ProviderErrorState) {
	if len(spec.CodePaths) == 0 {
		return "", ""
	}
	if len(body) > providerErrorBodyLimit {
		body = body[:providerErrorBodyLimit]
	}
	var doc any
	if json.Unmarshal(body, &doc) != nil {
		return "", ""
	}
	first := ""
	for _, path := range spec.CodePaths {
		code, ok := jsonPathString(doc, path)
		if !ok || !providerErrorCode.MatchString(code) {
			continue
		}
		if state, known := spec.Codes[code]; known {
			return code, state
		}
		if first == "" {
			first = code
		}
	}
	return first, ""
}

func jsonPathString(doc any, path string) (string, bool) {
	cur := doc
	for _, seg := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			cur = node[seg]
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(node) {
				return "", false
			}
			cur = node[i]
		default:
			return "", false
		}
	}
	s, ok := cur.(string)
	return s, ok
}

func defaultErrorState(status int) ProviderErrorState {
	switch {
	case status == 401 || status == 403:
		return ErrorCredentialRejected
	case status == 402:
		return ErrorQuotaExhausted
	case status == 429:
		return ErrorRateLimited
	case status >= 500:
		return ErrorUnavailable
	default:
		return ErrorRequestRejected
	}
}

func providerErrorOutcome(providerID string, status int, perr ProviderError) providerOutcome {
	detail := fmt.Sprintf("HTTP %d", status)
	if perr.Code != "http_"+strconv.Itoa(status) {
		detail += " " + perr.Code
	}
	return providerOutcome{
		providerID: providerID,
		reason:     string(perr.State),
		detail:     detail,
		errorCode:  perr.Code,
		httpStatus: status,
	}
}

func errorSpecRule(spec *ErrorSpec) CatalogRule {
	if spec == nil {
		return ""
	}
	if len(spec.CodePaths) == 0 && len(spec.Codes) == 0 && len(spec.Statuses) == 0 {
		return RuleErrorsEmpty
	}
	if len(spec.Codes) > 0 && len(spec.CodePaths) == 0 {
		return RuleErrorCodesWithoutPaths
	}
	for _, path := range spec.CodePaths {
		for _, seg := range strings.Split(path, ".") {
			if strings.TrimSpace(seg) == "" {
				return RuleErrorCodePathInvalid
			}
		}
	}
	for code, state := range spec.Codes {
		if !providerErrorCode.MatchString(code) {
			return RuleErrorCodeInvalid
		}
		if !state.valid() {
			return RuleErrorStateUnknown
		}
	}
	for status, state := range spec.Statuses {
		if status < 400 || status > 599 {
			return RuleErrorStatusOutOfRange
		}
		if !state.valid() {
			return RuleErrorStateUnknown
		}
	}
	return ""
}
