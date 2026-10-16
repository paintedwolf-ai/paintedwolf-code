package failure

import "errors"

// ErrProviderToolCallUnparsed marks a native tool call the provider's own
// parser dropped because its arguments were not valid JSON.
var ErrProviderToolCallUnparsed = errors.New("provider could not parse the model's tool call arguments")

// ProviderToolCallUnparsedError reports a dropped native tool call. The
// provider names neither the tool nor the arguments, so the error carries no
// call identity; Cause is the provider's original error.
type ProviderToolCallUnparsedError struct {
	ProviderID string
	Model      string
	Cause      error
}

func (e *ProviderToolCallUnparsedError) Error() string {
	if e == nil || e.Cause == nil {
		return ErrProviderToolCallUnparsed.Error()
	}
	return e.Cause.Error()
}

func (e *ProviderToolCallUnparsedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *ProviderToolCallUnparsedError) Is(target error) bool {
	return target == ErrProviderToolCallUnparsed
}

// AsProviderToolCallUnparsed reports whether err is a dropped native tool call.
func AsProviderToolCallUnparsed(err error) (*ProviderToolCallUnparsedError, bool) {
	var e *ProviderToolCallUnparsedError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}
