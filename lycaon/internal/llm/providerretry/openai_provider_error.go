package providerretry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/failure"
	openai "github.com/sashabaranov/go-openai"
)

type openAICompatErrorFields struct {
	Message       string `json:"message"`
	Status        string `json:"status"`
	Type          string `json:"type"`
	Param         string `json:"param"`
	Code          any    `json:"code"`
	NCtx          int    `json:"n_ctx"`
	NPromptTokens int    `json:"n_prompt_tokens"`
	MaxModelLen   int    `json:"max_model_len"`
	ContextLength int    `json:"context_length"`
	MaxContextLen int    `json:"max_context_length"`
	PromptTokens  int    `json:"prompt_tokens"`
	Details       []struct {
		Reason string `json:"reason"`
	} `json:"details"`
}

type openAICompatErrorBody struct {
	Error openAICompatErrorFields `json:"error"`
	openAICompatErrorFields
}

// ProviderHTTPError preserves structured response fields.
type ProviderHTTPError struct {
	Status       int
	Code         string
	Param        string
	Type         string
	Message      string
	MaxContext   int
	PromptTokens int
	Cause        error
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
		parsed := ParsedProviderError{
			Message: apiErr.Message,
			Code:    providerErrorCode(apiErr.Code),
			Param:   param,
			Type:    apiErr.Type,
		}
		var cause error = err
		if src, ok := parsed.IsContextOverflow(); ok {
			cause = &failure.ProviderContextTooSmallError{
				PromptTokens: parsed.PromptTokens,
				MaxContext:   parsed.MaxContext,
				Source:       src,
			}
		}
		return &ProviderHTTPError{
			Status:       apiErr.HTTPStatusCode,
			Code:         parsed.Code,
			Param:        param,
			Type:         apiErr.Type,
			Message:      apiErr.Message,
			MaxContext:   parsed.MaxContext,
			PromptTokens: parsed.PromptTokens,
			Cause:        cause,
		}
	}
	var reqErr *openai.RequestError
	if errors.As(err, &reqErr) {
		if parsed, ok := ParseOpenAICompatError(reqErr.Body); ok {
			var cause error = err
			if src, overflow := parsed.IsContextOverflow(); overflow {
				cause = &failure.ProviderContextTooSmallError{
					PromptTokens: parsed.PromptTokens,
					MaxContext:   parsed.MaxContext,
					Source:       src,
				}
			}
			return &ProviderHTTPError{
				Status:       reqErr.HTTPStatusCode,
				Code:         parsed.Code,
				Param:        parsed.Param,
				Type:         parsed.Type,
				Message:      parsed.Message,
				MaxContext:   parsed.MaxContext,
				PromptTokens: parsed.PromptTokens,
				Cause:        cause,
			}
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
	Status       string
	MaxContext   int
	PromptTokens int
}

// IsContextOverflow reports whether the provider error structurally classifies
// as a context-window overflow via its type or code field.
func (p ParsedProviderError) IsContextOverflow() (source string, ok bool) {
	for _, candidate := range []string{p.Type, p.Code, p.Status} {
		switch strings.ToLower(strings.TrimSpace(candidate)) {
		case "exceed_context_size_error":
			return "exceed_context_size_error", true
		case "context_length_exceeded":
			return "context_length_exceeded", true
		}
	}
	return "", false
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
	fields := env.Error
	if fields.Message == "" && fields.Code == nil && fields.Type == "" && fields.Status == "" {
		fields = env.openAICompatErrorFields
	}
	msg := strings.TrimSpace(fields.Message)
	code := providerErrorCode(fields.Code)
	typ := strings.TrimSpace(fields.Type)
	status := strings.TrimSpace(fields.Status)
	if msg == "" && code == "" && typ == "" && status == "" {
		return ParsedProviderError{}, false
	}
	for _, detail := range fields.Details {
		reason := strings.TrimSpace(detail.Reason)
		if reason == "" {
			continue
		}
		if msg != "" {
			msg += " (" + reason + ")"
		} else {
			msg = reason
		}
		break
	}
	parsed := ParsedProviderError{
		Message:      msg,
		Code:         code,
		Param:        strings.TrimSpace(fields.Param),
		Type:         typ,
		Status:       status,
		MaxContext:   firstPositiveInt(fields.NCtx, fields.MaxModelLen, fields.ContextLength, fields.MaxContextLen, env.openAICompatErrorFields.NCtx),
		PromptTokens: firstPositiveInt(fields.NPromptTokens, fields.PromptTokens, env.openAICompatErrorFields.NPromptTokens),
	}
	return parsed, true
}

func firstPositiveInt(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

func providerErrorCode(code any) string {
	if code == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(code))
}
