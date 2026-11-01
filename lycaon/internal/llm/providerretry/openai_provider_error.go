package providerretry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/failure"
	openai "github.com/sashabaranov/go-openai"
)

type openAICompatErrorBody struct {
	Error struct {
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
	} `json:"error"`
	NCtx          int `json:"n_ctx"`
	NPromptTokens int `json:"n_prompt_tokens"`
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
		populateOverflowCounts(&parsed)
		cause := err
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
			cause := err
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
	msg := strings.TrimSpace(env.Error.Message)
	code := providerErrorCode(env.Error.Code)
	typ := strings.TrimSpace(env.Error.Type)
	status := strings.TrimSpace(env.Error.Status)
	if msg == "" && code == "" && typ == "" {
		return ParsedProviderError{}, false
	}
	for _, detail := range env.Error.Details {
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
		Param:        strings.TrimSpace(env.Error.Param),
		Type:         typ,
		Status:       status,
		MaxContext:   firstPositiveInt(env.Error.NCtx, env.Error.MaxModelLen, env.Error.ContextLength, env.Error.MaxContextLen, env.NCtx),
		PromptTokens: firstPositiveInt(env.Error.NPromptTokens, env.Error.PromptTokens, env.NPromptTokens),
	}
	populateOverflowCounts(&parsed)
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

var (
	overflowWindowPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:available\s+)?context\s+(?:size|length|window)\s*(?:is|of|=|:)?\s*\(?(\d+)\b`),
		regexp.MustCompile(`(?i)maximum\s+context\s+length\s+(?:is|of)\s+(\d+)\b`),
		regexp.MustCompile(`(?i)(?:n_ctx|max_model_len|max_context_length)\s*(?:is|of|=|:)?\s*\(?(\d+)\b`),
	}
	overflowPromptPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:request|prompt)\s*\((\d+)\s*tokens\)`),
		regexp.MustCompile(`(?i)(?:resulted\s+in|requested|passed|prompt\s+is)\s+(\d+)\s*(?:prompt\s+)?tokens`),
		regexp.MustCompile(`(?i)n_prompt_tokens\s*(?:is|=|:)?\s*\(?(\d+)\b`),
	}
)

func populateOverflowCounts(parsed *ParsedProviderError) {
	if parsed == nil {
		return
	}
	if _, ok := parsed.IsContextOverflow(); !ok {
		return
	}
	if parsed.MaxContext <= 0 {
		for _, re := range overflowWindowPatterns {
			if m := re.FindStringSubmatch(parsed.Message); len(m) == 2 {
				if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
					parsed.MaxContext = n
					break
				}
			}
		}
	}
	if parsed.PromptTokens <= 0 {
		for _, re := range overflowPromptPatterns {
			if m := re.FindStringSubmatch(parsed.Message); len(m) == 2 {
				if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
					parsed.PromptTokens = n
					break
				}
			}
		}
	}
}

func providerErrorCode(code any) string {
	if code == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(code))
}
