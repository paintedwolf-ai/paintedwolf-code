package turnexecution

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/session/lifecycle"
)

func mapPromptRunError(err error) error {
	if errors.Is(err, context.Canceled) {
		return lifecycle.ErrStopping
	}
	return err
}
