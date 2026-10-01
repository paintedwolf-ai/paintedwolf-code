package providerretry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

type openAICompatErrorBody struct {
	Error struct {
		Message string `json:"message"`
		Status  string `json:"status"`
		Type    string `json:"type"`
		Param   string `json:"param"`
		Code    any    `json:"code"`
		Details []struct {
			Reason string `json:"reason"`
		} `json:"details"`
	} `json:"error"`
}

// ProviderHTTPError preserves structured response fields.
type ProviderHTTPError struct {
	Status  int
	Code    string
	Param   string
	Type    string
	Message string
	Cause   error
}

func (e *ProviderHTTPError) Error() string {
	if e == nil {
		return "provider request failed"
	}
	if e.Message != "" {
		return fmt.Sprintf("openai error %d: %s", e.Status, e.Message)
	}
	if e.Cause != nil {
		return fmt.Sprintf("openai error %d: %v", e.Status, e.Cause)
	}
	return fmt.Sprintf("openai error %d", e.Status)
}

func (e *ProviderHTTPError) Unwrap() error { return e.Cause }

func IsRequestRejected(err error) bool {
	var httpErr *ProviderHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Status == 400 || httpErr.Status == 422
	}
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		return apiErr.HTTPStatusCode == 400 || apiErr.HTTPStatusCode == 422
	}
	var reqErr *openai.RequestError
	if errors.As(err, &reqErr) {
		return reqErr.HTTPStatusCode == 400 || reqErr.HTTPStatusCode == 422
	}
	return false
}

// FormatOpenAIProviderError returns short provider errors.
// Some compatible endpoints wrap errors in a top-level array.
func FormatOpenAIProviderError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		param := ""
		if apiErr.Param != nil {
			param = *apiErr.Param
		}
		return &ProviderHTTPError{Status: apiErr.HTTPStatusCode, Code: providerErrorCode(apiErr.Code), Param: param, Type: apiErr.Type, Message: apiErr.Message, Cause: err}
	}
	var reqErr *openai.RequestError
	if errors.As(err, &reqErr) {
		if parsed, ok := ParseOpenAICompatError(reqErr.Body); ok {
			return &ProviderHTTPError{Status: reqErr.HTTPStatusCode, Code: parsed.Code, Param: parsed.Param, Type: parsed.Type, Message: parsed.Message, Cause: err}
		}
		if len(bytes.TrimSpace(reqErr.Body)) > 0 {
			return &ProviderHTTPError{Status: reqErr.HTTPStatusCode, Message: strings.TrimSpace(string(reqErr.Body)), Cause: err}
		}
		return &ProviderHTTPError{Status: reqErr.HTTPStatusCode, Cause: reqErr.Err}
	}
	return fmt.Errorf("openai request failed: %w", err)
}

type ParsedProviderError struct {
	Message string
	Code    string
	Param   string
	Type    string
	// Status is the structured symbolic status.
	Status string
}

func ParseOpenAICompatError(body []byte) (ParsedProviderError, bool) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return ParsedProviderError{}, false
	}
	if body[0] == '[' {
		var wrapped []openAICompatErrorBody
		if err := json.Unmarshal(body, &wrapped); err != nil || len(wrapped) == 0 {
			return ParsedProviderError{}, false
		}
		return fieldsFromErrorEnvelope(wrapped[0])
	}
	var env openAICompatErrorBody
	if err := json.Unmarshal(body, &env); err != nil {
		return ParsedProviderError{}, false
	}
	return fieldsFromErrorEnvelope(env)
}

func fieldsFromErrorEnvelope(env openAICompatErrorBody) (ParsedProviderError, bool) {
	msg := strings.TrimSpace(env.Error.Message)
	if msg == "" {
		return ParsedProviderError{}, false
	}
	for _, detail := range env.Error.Details {
		reason := strings.TrimSpace(detail.Reason)
		if reason == "" {
			continue
		}
		msg += " (" + reason + ")"
		break
	}
	return ParsedProviderError{
		Message: msg,
		Code:    providerErrorCode(env.Error.Code),
		Param:   strings.TrimSpace(env.Error.Param),
		Type:    strings.TrimSpace(env.Error.Type),
		Status:  strings.TrimSpace(env.Error.Status),
	}, true
}

func providerErrorCode(code any) string {
	if code == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(code))
}
