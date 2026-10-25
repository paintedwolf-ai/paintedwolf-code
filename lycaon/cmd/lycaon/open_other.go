//go:build !darwin && !linux

package main

import (
	"context"
	"errors"
)

func startApp(context.Context) (appStart, error) {
	return nil, errors.New("the Painted Wolf Code app is not running; start it, then run this command again")
}
