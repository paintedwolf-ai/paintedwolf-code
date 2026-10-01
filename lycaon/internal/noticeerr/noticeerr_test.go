package noticeerr

import (
	"errors"
	"fmt"
	"testing"

	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestCodeOfReadsThroughAWrappedChain(t *testing.T) {
	sentinel := NewSentinel("boom", wire.NoticeCodeProviderNotConfigured)
	wrapped := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", error(sentinel)))

	code, ok := CodeOf(wrapped)
	if !ok || code != wire.NoticeCodeProviderNotConfigured {
		t.Fatalf("CodeOf = (%q, %v), want provider_not_configured", code, ok)
	}
}

func TestSentinelStaysComparable(t *testing.T) {
	var target error = NewSentinel("stop", wire.NoticeCodeWorktreeStale)
	wrapped := fmt.Errorf("context: %w", target)

	if !errors.Is(wrapped, target) {
		t.Fatal("errors.Is must still match a coded sentinel through a wrap")
	}
}

func TestUncodedErrorsReportNoCode(t *testing.T) {
	for _, err := range []error{nil, errors.New("plain"), fmt.Errorf("wrapped: %w", errors.New("plain"))} {
		if code, ok := CodeOf(err); ok {
			t.Fatalf("CodeOf(%v) = %q, want no code", err, code)
		}
	}
}

func TestWithCodeRestoresClassificationAfterTypeLoss(t *testing.T) {
	original := NewSentinel("provider missing", wire.NoticeCodeProviderNotConfigured)
	code, ok := CodeOf(original)
	if !ok {
		t.Fatal("expected the original to carry a code")
	}

	// Persistence drops the error type.
	flattened := errors.New(original.Error())
	if _, ok := CodeOf(flattened); ok {
		t.Fatal("a flattened error must not carry a code on its own")
	}

	restored := WithCode(flattened, code)
	got, ok := CodeOf(restored)
	if !ok || got != wire.NoticeCodeProviderNotConfigured {
		t.Fatalf("CodeOf(restored) = (%q, %v), want provider_not_configured", got, ok)
	}
	if restored.Error() != original.Error() {
		t.Fatalf("restored message = %q, want %q", restored.Error(), original.Error())
	}
	if !errors.Is(restored, flattened) {
		t.Fatal("WithCode must keep the wrapped error reachable")
	}
}

func TestWithCodeIsInertWithoutInput(t *testing.T) {
	if got := WithCode(nil, wire.NoticeCodePromptFailed); got != nil {
		t.Fatalf("WithCode(nil, code) = %v, want nil", got)
	}
	plain := errors.New("plain")
	if got := WithCode(plain, ""); !errors.Is(got, plain) {
		t.Fatal("an empty code must return the error unchanged")
	}
}
