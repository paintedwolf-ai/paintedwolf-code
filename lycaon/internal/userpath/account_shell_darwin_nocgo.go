//go:build darwin && !cgo

package userpath

import (
	"context"
	"os"
)

func defaultAccountShell(ctx context.Context) (string, error) {
	if s := os.Getenv("SHELL"); s != "" {
		return s, nil
	}
	return "/bin/zsh", nil
}
