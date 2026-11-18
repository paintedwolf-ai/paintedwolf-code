package conditions

import (
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/vocabulary"
)

var ErrForbiddenCondition = errors.New("forbidden condition")

func checkForbiddenRegister(name string) error {
	if vocabulary.IsForbidden(name) {
		return fmt.Errorf("%w: %s", ErrForbiddenCondition, name)
	}
	return nil
}
