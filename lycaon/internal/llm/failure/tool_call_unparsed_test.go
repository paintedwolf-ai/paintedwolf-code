package failure

import (
	"errors"
	"fmt"
	"testing"
)

func TestProviderToolCallUnparsedKeepsOriginalError(t *testing.T) {
	cause := errors.New("ollama: unexpected end of JSON input")
	err := fmt.Errorf("stream: %w", &ProviderToolCallUnparsedError{ProviderID: "desktop", Model: "m", Cause: cause})
	got, ok := AsProviderToolCallUnparsed(err)
	if !ok || got.ProviderID != "desktop" || !errors.Is(err, ErrProviderToolCallUnparsed) || !errors.Is(err, cause) {
		t.Fatalf("typed failure lost through wrapping: %v", err)
	}
	if err.Error() != "stream: ollama: unexpected end of JSON input" {
		t.Fatalf("message = %q, want the provider's original text", err.Error())
	}
	var empty *ProviderToolCallUnparsedError
	if empty.Error() != ErrProviderToolCallUnparsed.Error() || empty.Unwrap() != nil {
		t.Fatal("nil failure must describe itself without a cause")
	}
	if _, ok := AsProviderToolCallUnparsed(cause); ok {
		t.Fatal("untyped provider error classified as a dropped call")
	}
}
