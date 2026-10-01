package bedrock

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerauth"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/testutil"
)

type rateSDKHTTP func(*http.Request) (*http.Response, error)

func (f rateSDKHTTP) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestProviderRateBedrockUsesSharedAdmissionAfterSDKThrottling(t *testing.T) {
	directory := t.TempDir()
	policy := providerretry.ProviderHTTPRetry{RateLimit: &providerretry.ProviderRatePolicy{Adaptive: true, Scope: "provider", InitialMs: 1, MaxBackoffMs: 1, RecoverySuccesses: 2}}.BindRateGate(providerretry.NewProviderRateGate(directory), "https://provider.example:443")
	provider := New("fixture", "us-east-1", "", []modelinfo.Entry{{ID: "model-a"}}).WithHTTPRetry(policy)
	calls := 0
	client := rateSDKHTTP(func(request *http.Request) (*http.Response, error) {
		calls++
		status, body := 429, `{"message":"fixture throttle"}`
		headers := http.Header{"X-Amzn-Errortype": []string{"ThrottlingException"}}
		if calls == 8 {
			status = 200
			headers = http.Header{}
			body = `{"output":{"message":{"role":"assistant","content":[{"text":"done"}]}},"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2},"metrics":{"latencyMs":1}}`
		}
		return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})
	provider.client = bedrockruntime.NewFromConfig(aws.Config{
		Region: "us-east-1", Credentials: aws.AnonymousCredentials{}, HTTPClient: client,
		Retryer: func() aws.Retryer { return aws.NopRetryer{} },
	}, providerauth.BedrockRuntimeOptions("fixture-token")...)
	completion, err := provider.Complete(t.Context(), modelcall.CompletionRequest{Model: "model-a"})
	testutil.FailErr(t, "complete SDK request after repeated throttling", err)
	if calls != 8 || completion.Content != "done" {
		t.Fatalf("calls=%d completion=%+v", calls, completion)
	}
	files, err := filepath.Glob(filepath.Join(directory, "*.json"))
	testutil.FailErr(t, "find shared SDK observations", err)
	if len(files) != 1 {
		t.Fatalf("rate state files=%v", files)
	}
	data, err := os.ReadFile(files[0])
	testutil.FailErr(t, "read shared SDK observations", err)
	var state struct {
		Limited  int64 `json:"rate_limits"`
		Accepted int64 `json:"accepted"`
	}
	testutil.FailErr(t, "decode shared SDK observations", json.Unmarshal(data, &state))
	if state.Limited != 7 || state.Accepted != 1 {
		t.Fatalf("SDK bypassed shared rate gate: %+v", state)
	}
}
