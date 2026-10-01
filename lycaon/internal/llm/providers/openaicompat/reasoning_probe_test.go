package openaicompat

import (
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/providerretry"
	openai "github.com/sashabaranov/go-openai"
)

func TestReasoningProbeCache(t *testing.T) {
	t.Cleanup(ResetReasoningProbeForTest)
	if got := cachedReasoningFallback("p", "m"); got != reasoningFallbackNone {
		t.Fatalf("empty cache = %v, want none", got)
	}
	markReasoningFallback("p", "m", reasoningFallbackOmit)
	if got := cachedReasoningFallback("p", "m"); got != reasoningFallbackOmit {
		t.Fatalf("cached = %v, want omit", got)
	}
	markReasoningFallback("p", "m", reasoningFallbackOff)
	if got := cachedReasoningFallback("p", "m"); got != reasoningFallbackOff {
		t.Fatalf("cached = %v, want off after escalation", got)
	}
	// A later omit must not walk the ladder back down inside the window; the
	// model already told us omitting is not enough.
	markReasoningFallback("p", "m", reasoningFallbackOmit)
	if got := cachedReasoningFallback("p", "m"); got != reasoningFallbackOff {
		t.Fatalf("cached = %v, want off retained", got)
	}
}

func TestReasoningProbeTTLExpiry(t *testing.T) {
	t.Cleanup(ResetReasoningProbeForTest)
	key := providerModelKey{providerID: "p", model: "m"}
	reasoningUnsupported.Store(key, reasoningProbeEntry{
		fallback: reasoningFallbackOmit,
		expiry:   time.Now().Add(-time.Second),
	})
	if got := cachedReasoningFallback("p", "m"); got != reasoningFallbackNone {
		t.Fatalf("expired probe entry = %v, must clear", got)
	}
	if _, ok := reasoningUnsupported.Load(key); ok {
		t.Fatal("expired entry should be deleted on read")
	}
}

// Function tools require the explicit "none" fallback value.
func TestReasoningFallbackLadder(t *testing.T) {
	needsOff := providerretry.FormatOpenAIProviderError(&openai.RequestError{
		HTTPStatusCode: 400,
		Body:           []byte(`{"error":{"message":"Function tools with reasoning_effort are not supported for gpt-5.6-terra in /v1/chat/completions. To use function tools, use /v1/responses or set reasoning_effort to 'none'."}}`),
	})
	unknownParam := providerretry.FormatOpenAIProviderError(&openai.RequestError{
		HTTPStatusCode: 400,
		Body:           []byte(`{"error":{"message":"Unknown parameter: reasoning_effort"}}`),
	})

	cases := []struct {
		name      string
		err       error
		current   reasoningFallback
		hasOff    bool
		want      reasoningFallback
		wantRetry bool
	}{
		{name: "request rejection starts with omit regardless of copy", err: needsOff, current: reasoningFallbackNone, hasOff: true, want: reasoningFallbackOmit, wantRetry: true},
		{name: "needs off without a token still omits", err: needsOff, current: reasoningFallbackNone, want: reasoningFallbackOmit, wantRetry: true},
		{name: "unknown parameter omits first", err: unknownParam, current: reasoningFallbackNone, hasOff: true, want: reasoningFallbackOmit, wantRetry: true},
		{name: "omit that still fails escalates", err: needsOff, current: reasoningFallbackOmit, hasOff: true, want: reasoningFallbackOff, wantRetry: true},
		{name: "omit is terminal without a token", err: needsOff, current: reasoningFallbackOmit, want: reasoningFallbackOmit},
		{name: "off is terminal", err: needsOff, current: reasoningFallbackOff, hasOff: true, want: reasoningFallbackOff},
		{name: "unrelated failure never retries", err: errors.New("dial tcp"), current: reasoningFallbackNone, hasOff: true, want: reasoningFallbackNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, retry := nextReasoningFallback(tc.err, tc.current, tc.hasOff)
			if retry != tc.wantRetry {
				t.Fatalf("retry = %v, want %v", retry, tc.wantRetry)
			}
			if got != tc.want {
				t.Fatalf("fallback = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReasoningFallbackIgnoresProviderMessageText(t *testing.T) {
	for _, message := range []string{
		"Unknown parameter: reasoning_effort",
		"unrelated localized rejection text",
	} {
		err := providerretry.FormatOpenAIProviderError(&openai.RequestError{
			HTTPStatusCode: 400,
			Body:           []byte(`{"error":{"message":"` + message + `"}}`),
		})
		got, retry := nextReasoningFallback(err, reasoningFallbackNone, true)
		if !retry || got != reasoningFallbackOmit {
			t.Fatalf("message %q: fallback = %v, retry = %v", message, got, retry)
		}
	}
}
