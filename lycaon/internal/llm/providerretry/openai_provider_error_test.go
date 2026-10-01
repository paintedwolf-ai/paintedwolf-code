package providerretry

import (
	"errors"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

func TestParseOpenAICompatErrorBody_GeminiArray(t *testing.T) {
	body := []byte(`[{
  "error": {
    "code": 403,
    "message": "Requests to this API generativelanguage.googleapis.com method google.ai.generativelanguage.v1main.GenerativeService.GenerateContent are blocked.",
    "status": "PERMISSION_DENIED",
    "details": [
      {
        "@type": "type.googleapis.com/google.rpc.ErrorInfo",
        "reason": "API_KEY_SERVICE_BLOCKED",
        "domain": "googleapis.com"
      }
    ]
  }
}]`)

	parsed, ok := ParseOpenAICompatError(body)
	if !ok {
		t.Fatal("expected parsed gemini error body")
	}
	msg := parsed.Message
	if msg != "Requests to this API generativelanguage.googleapis.com method google.ai.generativelanguage.v1main.GenerativeService.GenerateContent are blocked. (API_KEY_SERVICE_BLOCKED)" {
		t.Fatalf("msg = %q", msg)
	}
}

func TestFormatOpenAIProviderError_GeminiRequestError(t *testing.T) {
	body := []byte(`[{"error":{"code":403,"message":"blocked","details":[{"reason":"API_KEY_SERVICE_BLOCKED"}]}}]`)
	err := FormatOpenAIProviderError(&openai.RequestError{
		HTTPStatus:     "403 Forbidden",
		HTTPStatusCode: 403,
		Err:            errors.New("json: cannot unmarshal array into Go value of type openai.ErrorResponse"),
		Body:           body,
	})
	got := err.Error()
	if got != "openai error 403: blocked (API_KEY_SERVICE_BLOCKED)" {
		t.Fatalf("formatted = %q", got)
	}
}

func TestFormatOpenAIProviderError_OpenAIAPIError(t *testing.T) {
	err := FormatOpenAIProviderError(&openai.APIError{
		HTTPStatusCode: 401,
		Message:        "invalid key",
	})
	if err.Error() != "openai error 401: invalid key" {
		t.Fatalf("formatted = %q", err)
	}
}

// Provider error codes are strings.
func TestFormatOpenAIProviderError_StringErrorCode(t *testing.T) {
	body := []byte(`{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","type":"invalid_request_error","param":"max_tokens","code":"unsupported_parameter"}}`)
	err := FormatOpenAIProviderError(&openai.RequestError{
		HTTPStatusCode: 400,
		Body:           body,
	})
	want := "openai error 400: Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead."
	if err.Error() != want {
		t.Fatalf("formatted = %q, want the message alone", err)
	}
	var providerErr *ProviderHTTPError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %T, want ProviderHTTPError", err)
	}
	if providerErr.Code != "unsupported_parameter" || providerErr.Param != "max_tokens" || providerErr.Type != "invalid_request_error" {
		t.Fatalf("structured fields = code:%q param:%q type:%q", providerErr.Code, providerErr.Param, providerErr.Type)
	}
}

// A request may retry without its response format only after a structured
// 400/422 rejection, never on prose, transport, or server errors.
func TestIsRequestRejectedGatesResponseFormatRetry(t *testing.T) {
	if IsRequestRejected(errors.New("openai error 400: response_format type json_schema is not supported")) {
		t.Fatal("untyped prose must not trigger a retry")
	}
	if !IsRequestRejected(&openai.APIError{
		HTTPStatusCode: 400,
		Message:        "Invalid parameter: 'response_format' of type 'json_schema' is not supported with this model",
	}) {
		t.Fatal("expected unsupported for APIError")
	}
	if IsRequestRejected(errors.New("dial tcp: connection refused")) {
		t.Fatal("network errors must not look like format reject")
	}
	if IsRequestRejected(&openai.APIError{
		HTTPStatusCode: 500,
		Message:        "response_format json_schema not supported",
	}) {
		t.Fatal("5xx must not trigger format retry")
	}
}
