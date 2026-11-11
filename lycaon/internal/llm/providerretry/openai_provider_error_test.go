package providerretry

import (
	"encoding/json"
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

func TestOverflowCapacityDependsOnStructuredFieldsRatherThanDiagnostics(t *testing.T) {
	for _, message := range []string{"", "maximum context length is 12 tokens; requested 999999 tokens", "n_ctx=1 n_prompt_tokens=2", "context window: 999999"} {
		for _, structured := range []bool{false, true} {
			envelope := map[string]any{"code": "context_length_exceeded", "message": message}
			if structured {
				envelope["n_ctx"], envelope["n_prompt_tokens"] = 8192, 9000
			}
			body, err := json.Marshal(map[string]any{"error": envelope})
			if err != nil {
				t.Fatalf("marshal fixture: %v", err)
			}
			parsed, ok := ParseOpenAICompatError(body)
			if !ok {
				t.Fatal("structured rejection was not parsed")
			}
			if _, overflow := parsed.IsContextOverflow(); !overflow {
				t.Fatal("structured rejection lost overflow classification")
			}
			window, prompt := 0, 0
			if structured {
				window, prompt = 8192, 9000
			}
			if parsed.MaxContext != window || parsed.PromptTokens != prompt {
				t.Fatalf("diagnostic changed facts: %+v", parsed)
			}
		}
	}
}

func TestFormatOpenAIProviderError_FlatTopLevelErrorBody(t *testing.T) {
	body := []byte(`{"message":"Model qwen-3-235b-a22b-instruct-2507 does not exist or you do not have access to it.","type":"not_found_error","param":"model","code":"model_not_found"}`)
	err := FormatOpenAIProviderError(&openai.RequestError{
		HTTPStatusCode: 404,
		Body:           body,
	})
	var providerErr *ProviderHTTPError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %T, want ProviderHTTPError", err)
	}
	if providerErr.Code != "model_not_found" || providerErr.Param != "model" || providerErr.Type != "not_found_error" {
		t.Fatalf("structured fields = %+v", providerErr)
	}
	parsed, ok := ParseOpenAICompatError(body)
	if !ok {
		t.Fatal("expected ParseOpenAICompatError to succeed")
	}
	if got := ModelRefusalEvidenceFor(404, parsed, DefaultModelRefusalCodes()); got != RefusalEvidenceModelIdentity {
		t.Fatalf("ModelRefusalEvidenceFor = %q, want %q", got, RefusalEvidenceModelIdentity)
	}
}
