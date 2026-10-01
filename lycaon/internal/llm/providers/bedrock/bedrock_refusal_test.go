package bedrock

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
)

func strPtr(s string) *string { return &s }

func TestBedrockModelRefusalClassification(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode string
	}{
		{
			name:     "missing model or inference profile",
			err:      &brtypes.ResourceNotFoundException{Message: strPtr("no such model")},
			wantCode: "ResourceNotFoundException",
		},
		{
			name:     "authenticated but not authorized for this model",
			err:      &brtypes.AccessDeniedException{Message: strPtr("not authorized to perform bedrock:InvokeModel")},
			wantCode: "AccessDeniedException",
		},
		{
			// Bedrock returns ValidationException both for a bad model id and for
			// a body it will not accept, so it never marks a pair unusable.
			name: "validation is ambiguous and never a refusal",
			err:  &brtypes.ValidationException{Message: strPtr("The provided model identifier is invalid")},
		},
		{
			name: "throttling is about load, not the pair",
			err:  &brtypes.ThrottlingException{Message: strPtr("slow down")},
		},
		{
			name: "service unavailable is about health",
			err:  &brtypes.ServiceUnavailableException{Message: strPtr("try later")},
		},
		{
			name: "unrelated error",
			err:  errors.New("boom"),
		},
		{name: "no error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bedrockModelRefusal("bedrock-1", "anthropic.claude-3", tc.err)
			if tc.wantCode == "" {
				if got != nil {
					t.Fatalf("refusal = %v, want none", got)
				}
				return
			}
			refusal, ok := providerretry.AsModelRefused(got)
			if !ok {
				t.Fatalf("err = %v, want a ModelRefusedError", got)
			}
			if refusal.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", refusal.Code, tc.wantCode)
			}
			if refusal.ProviderID != "bedrock-1" || refusal.Model != "anthropic.claude-3" {
				t.Fatalf("refusal names %q/%q", refusal.ProviderID, refusal.Model)
			}
			if !errors.Is(got, providerretry.ErrModelRefused) {
				t.Fatal("refusal must match ErrModelRefused")
			}
		})
	}
}

// Throttling and unavailability stay on the retry schedule, not the refusal path.
func TestBedrockRefusalKeepsRetryMapIntact(t *testing.T) {
	throttled := &brtypes.ThrottlingException{Message: strPtr("slow down")}
	if got := bedrockRetryStatus(throttled); got != http.StatusTooManyRequests {
		t.Fatalf("throttle status = %d, want 429", got)
	}
	unavailable := &brtypes.ServiceUnavailableException{Message: strPtr("try later")}
	if got := bedrockRetryStatus(unavailable); got != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status = %d, want 503", got)
	}
}

// Only AccessDenied names the model itself. ResourceNotFound also covers an
// unsubmitted use-case form, which clears outside this process, so it must
// refuse the turn without being remembered.
func TestBedrockRefusalStickinessSplitsByFault(t *testing.T) {
	denied := bedrockModelRefusal("bedrock-1", "m", &brtypes.AccessDeniedException{Message: strPtr("no access")})
	refusal, ok := providerretry.AsModelRefused(denied)
	if !ok {
		t.Fatal("AccessDeniedException must refuse")
	}
	if !refusal.Evidence.Sticky() {
		t.Fatal("AccessDeniedException names the model, so its refusal must be remembered")
	}

	notFound := bedrockModelRefusal("bedrock-1", "m", &brtypes.ResourceNotFoundException{
		Message: strPtr("Model use case details have not been submitted for this account"),
	})
	refusal, ok = providerretry.AsModelRefused(notFound)
	if !ok {
		t.Fatal("ResourceNotFoundException must refuse")
	}
	if refusal.Evidence.Sticky() {
		t.Fatal("ResourceNotFoundException conflates account state with model identity; it must not be remembered")
	}

	gate := providerretry.NewModelRefusalGate()
	gate.Note("bedrock-1", "m", notFound)
	if _, remembered := gate.Refused("bedrock-1", "m"); remembered {
		t.Fatal("the refusal gate must not hold a ResourceNotFound refusal")
	}
	gate.Note("bedrock-1", "m", denied)
	if _, remembered := gate.Refused("bedrock-1", "m"); !remembered {
		t.Fatal("the refusal gate must hold an AccessDenied refusal")
	}
}

// TestBedrockResponseStatusIsAdvisory pins that an unwrapped typed fault still
// refuses. The status is recorded when the SDK carried one and is never the
// thing that decides.
func TestBedrockResponseStatusIsAdvisory(t *testing.T) {
	bare := &brtypes.ResourceNotFoundException{Message: strPtr("gone")}
	refusal, ok := providerretry.AsModelRefused(bedrockModelRefusal("bedrock-1", "m", bare))
	if !ok {
		t.Fatal("an unwrapped typed fault must still refuse")
	}
	if refusal.Status != 0 {
		t.Fatalf("status = %d, want 0 when the SDK carried none", refusal.Status)
	}

	wrapped := fmt.Errorf("operation error: %w", &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: http.StatusNotFound}},
		Err:      bare,
	})
	refusal, ok = providerretry.AsModelRefused(bedrockModelRefusal("bedrock-1", "m", wrapped))
	if !ok {
		t.Fatal("a wrapped typed fault must refuse")
	}
	if refusal.Status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", refusal.Status)
	}
}
