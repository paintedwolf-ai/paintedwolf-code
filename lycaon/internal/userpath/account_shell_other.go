//go:build !darwin

package userpath

import (
	"context"
	"errors"
)

func defaultAccountShell(context.Context) (string, error) {
	return "", errors.New("account shell lookup is unavailable on this platform")
}
