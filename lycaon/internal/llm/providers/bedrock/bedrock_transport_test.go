package bedrock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
)

type headerTimeoutError struct{}

func (headerTimeoutError) Error() string   { return "net/http: timeout awaiting response headers" }
func (headerTimeoutError) Timeout() bool   { return true }
func (headerTimeoutError) Temporary() bool { return true }

func answered(status int, fault error) error {
	return fmt.Errorf("operation error Bedrock Runtime: Converse: %w", &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
		Err:      fault,
	})
}

func sendFailed(err error) error {
	return fmt.Errorf("operation error Bedrock Runtime: Converse: %w", &smithyhttp.RequestSendError{Err: err})
}

func TestClassifyConverseReadsDeliveryAndTypedFaults(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	cases := map[string]struct {
		ctx  context.Context
		obs  httpclient.RequestObservation
		err  error
		kind providerretry.FaultKind
		code int
	}{
		"never connected": {
			ctx: t.Context(), err: sendFailed(&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}),
			kind: providerretry.FaultUnreachable,
		},
		"connected, write never finished": {
			ctx: t.Context(), obs: httpclient.RequestObservation{GotConn: true},
			err:  sendFailed(&net.OpError{Op: "write", Net: "tcp", Err: syscall.ECONNRESET}),
			kind: providerretry.FaultUnreachable,
		},
		"delivered, no response headers": {
			ctx: t.Context(), obs: httpclient.RequestObservation{GotConn: true, WroteRequest: true},
			err:  sendFailed(headerTimeoutError{}),
			kind: providerretry.FaultSilent,
		},
		"throttled": {
			ctx: t.Context(), err: answered(400, &brtypes.ThrottlingException{Message: strPtr("slow")}),
			kind: providerretry.FaultRateLimited, code: http.StatusTooManyRequests,
		},
		"service unavailable": {
			ctx: t.Context(), err: answered(503, &brtypes.ServiceUnavailableException{Message: strPtr("busy")}),
			kind: providerretry.FaultCapacity, code: http.StatusServiceUnavailable,
		},
		"internal server fault": {
			ctx: t.Context(), err: answered(500, &brtypes.InternalServerException{Message: strPtr("oops")}),
			kind: providerretry.FaultCapacity, code: http.StatusServiceUnavailable,
		},
		"validation": {
			ctx: t.Context(), err: answered(400, &brtypes.ValidationException{Message: strPtr("bad body")}),
			kind: providerretry.FaultStatus, code: http.StatusBadRequest,
		},
		"caller cancellation outranks an answer": {
			ctx: canceled, err: answered(503, &brtypes.ServiceUnavailableException{Message: strPtr("busy")}),
			kind: providerretry.FaultCanceled,
		},
		"caller cancellation outranks delivery": {
			ctx: canceled, obs: httpclient.RequestObservation{GotConn: true, WroteRequest: true},
			err:  context.Canceled,
			kind: providerretry.FaultCanceled,
		},
	}
	for name, tc := range cases {
		fault, sent := classifyConverse(tc.ctx, tc.obs, time.Second, tc.err)
		if !sent || fault.Kind != tc.kind || fault.Status != tc.code {
			t.Fatalf("%s: fault = %s/%d sent=%v want %s/%d sent", name, fault.Kind, fault.Status, sent, tc.kind, tc.code)
		}
	}
	signing := errors.New("operation error Bedrock Runtime: Converse, sign request: bearer token with HTTP request requires HTTPS")
	if fault, sent := classifyConverse(t.Context(), httpclient.RequestObservation{}, 0, signing); sent {
		t.Fatalf("a failure before sending was classified as %s", fault.Kind)
	}
}

// converseServer is a Bedrock Runtime endpoint for the SDK to call. respond
// returns false to hold the request open until the client leaves or the test
// ends.
func converseServer(t *testing.T, respond func(http.ResponseWriter) bool) (srv *httptest.Server, calls *atomic.Int32, stop func()) {
	t.Helper()
	calls = &atomic.Int32{}
	release := make(chan struct{})
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// The server notices a client disconnect only once the body is read.
		_, _ = io.Copy(io.Discard, r.Body)
		if respond(w) {
			return
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	var once sync.Once
	stop = func() {
		once.Do(func() {
			close(release)
			srv.Close()
		})
	}
	t.Cleanup(stop)
	return srv, calls, stop
}

func serviceFault(status int, errType string) func(http.ResponseWriter) bool {
	return func(w http.ResponseWriter) bool {
		w.Header().Set("X-Amzn-Errortype", errType)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"message":"fixture"}`)
		return true
	}
}

func stall(http.ResponseWriter) bool { return false }

// transportProvider builds the production SDK client against endpoint, so the
// host transport, header bound, and disabled SDK retryer all run. SigV4
// credentials sign over plain HTTP; a bearer key refuses to.
func transportProvider(endpoint string, policy providerretry.ProviderHTTPRetry, headerTimeout time.Duration) *Provider {
	p := New("bedrock-1", "us-east-1", "", []modelinfo.Entry{{ID: "model-a"}}).WithHTTPRetry(policy)
	p.profile.CompleteResponseHeaderTimeout = headerTimeout
	p.loadConfig = func(context.Context, string) (aws.Config, error) {
		return aws.Config{
			Region:       "us-east-1",
			Credentials:  aws.CredentialsProviderFunc(fixtureCredentials),
			BaseEndpoint: aws.String(endpoint),
		}, nil
	}
	return p
}

func fixtureCredentials(context.Context) (aws.Credentials, error) {
	return aws.Credentials{AccessKeyID: "fixture", SecretAccessKey: "fixture"}, nil
}

func TestBedrockPreSendFailureIsNotATransportFault(t *testing.T) {
	srv, calls, _ := converseServer(t, stall)
	p := transportProvider(srv.URL, noRetries(), time.Minute)
	p.apiKey = "fixture-token" // bearer signing refuses plain HTTP before sending

	_, err := p.Complete(t.Context(), converseRequest)
	if err == nil {
		t.Fatal("unsigned request succeeded")
	}
	if _, ok := failure.AsProviderUnreachable(err); ok {
		t.Fatalf("a signing failure was reported as an unreachable endpoint: %v", err)
	}
	if _, ok := failure.AsProviderSilent(err); ok {
		t.Fatalf("a signing failure was reported as a silent endpoint: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("server calls = %d want 0", calls.Load())
	}
}

func noRetries() providerretry.ProviderHTTPRetry {
	return providerretry.ProviderHTTPRetry{Transport: &providerretry.ProviderTransportRetry{}}
}

var converseRequest = modelcall.CompletionRequest{Model: "model-a"}

func TestBedrockUnreachableEndpointIsATypedFault(t *testing.T) {
	srv, _, stop := converseServer(t, stall)
	endpoint := srv.URL
	stop()

	_, err := transportProvider(endpoint, noRetries(), time.Minute).Complete(t.Context(), converseRequest)
	unreachable, ok := failure.AsProviderUnreachable(err)
	if !ok {
		t.Fatalf("err = %v want ProviderUnreachableError", err)
	}
	if unreachable.ProviderID != "bedrock-1" || unreachable.Model != "model-a" || unreachable.Attempts != 1 {
		t.Fatalf("unreachable = %+v", unreachable)
	}
}

func TestBedrockSilentEndpointIsATypedFault(t *testing.T) {
	srv, calls, _ := converseServer(t, stall)

	_, err := transportProvider(srv.URL, noRetries(), 100*time.Millisecond).Complete(t.Context(), converseRequest)
	silent, ok := failure.AsProviderSilent(err)
	if !ok {
		t.Fatalf("err = %v want ProviderSilentError", err)
	}
	if silent.Attempts != 1 || calls.Load() != 1 {
		t.Fatalf("silent = %+v after %d server calls; want one attempt, one request", silent, calls.Load())
	}
}

func TestBedrockSilentEndpointFollowsTheTransportRetrySchedule(t *testing.T) {
	srv, calls, _ := converseServer(t, stall)
	policy := providerretry.ProviderHTTPRetry{Transport: &providerretry.ProviderTransportRetry{MaxRetries: 1, BackoffMs: []int{1}}}

	_, err := transportProvider(srv.URL, policy, 100*time.Millisecond).Complete(t.Context(), converseRequest)
	silent, ok := failure.AsProviderSilent(err)
	if !ok || silent.Attempts != 2 || calls.Load() != 2 {
		t.Fatalf("err = %v, %d server calls; want a silent fault after 2 attempts", err, calls.Load())
	}
}

func TestBedrockCallerDeadlineOutranksTransportFaults(t *testing.T) {
	srv, _, _ := converseServer(t, stall)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	_, err := transportProvider(srv.URL, noRetries(), time.Minute).Complete(ctx, converseRequest)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v want the caller's deadline", err)
	}
	if _, ok := failure.AsProviderSilent(err); ok {
		t.Fatal("a caller deadline was reported as a silent endpoint")
	}
	if _, ok := failure.AsProviderUnreachable(err); ok {
		t.Fatal("a caller deadline was reported as an unreachable endpoint")
	}
}

func TestBedrockExhaustedUnavailableIsOverloadedWithoutSDKRetries(t *testing.T) {
	srv, calls, _ := converseServer(t, serviceFault(http.StatusServiceUnavailable, "ServiceUnavailableException"))

	_, err := transportProvider(srv.URL, noRetries(), time.Minute).Complete(t.Context(), converseRequest)
	overloaded, ok := failure.AsProviderOverloaded(err)
	if !ok {
		t.Fatalf("err = %v want ProviderOverloadedError", err)
	}
	if overloaded.Status != http.StatusServiceUnavailable || calls.Load() != 1 {
		t.Fatalf("overloaded = %+v after %d server calls; the SDK must not retry on its own", overloaded, calls.Load())
	}
}

func TestBedrockValidationIsARejectionNotATransportFault(t *testing.T) {
	srv, calls, _ := converseServer(t, serviceFault(http.StatusBadRequest, "ValidationException"))
	policy := providerretry.ProviderHTTPRetry{Transport: &providerretry.ProviderTransportRetry{MaxRetries: 3, BackoffMs: []int{1}}}

	_, err := transportProvider(srv.URL, policy, time.Minute).Complete(t.Context(), converseRequest)
	rejected, ok := providerretry.AsProviderRequestRejected(err)
	if !ok || rejected.Status != http.StatusBadRequest {
		t.Fatalf("err = %v want a 400 request rejection", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("server calls = %d; an answered rejection must not take the transport schedule", calls.Load())
	}
}
