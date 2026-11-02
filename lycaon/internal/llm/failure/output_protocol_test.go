package failure

import (
	"errors"
	"testing"
)

func TestOutputProtocolErrorRetainsCause(t *testing.T) {
	cause := errors.New("bounded diagnostic")
	err := &ProviderOutputProtocolError{Cause: cause}
	if err.Error() != cause.Error() || !errors.Is(err, cause) {
		t.Fatal("lost diagnostic cause")
	}
	if (&ProviderOutputProtocolError{}).Error() != "provider output protocol violation" {
		t.Fatal("missing generic diagnostic")
	}
}
