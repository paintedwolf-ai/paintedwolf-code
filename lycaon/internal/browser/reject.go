package browser

import (
	"errors"

	"github.com/lycaon/lycaon/internal/browserengine"
)

// cdpUnavailable maps rod/CDP transport failures to BROWSER_UNAVAILABLE so the
// coordinator can branch on Code: instead of opaque "browser page: …" strings.
func cdpUnavailable(reason string, err error) error {
	if err == nil {
		return nil
	}
	rej := &browserengine.RejectError{}
	if errors.As(err, &rej) {
		return rej
	}
	return browserengine.Reject("BROWSER_UNAVAILABLE", map[string]any{
		"reason": reason,
		"detail": err.Error(),
	})
}
