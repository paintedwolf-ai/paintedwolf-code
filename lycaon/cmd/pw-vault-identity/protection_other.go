//go:build !linux

package main

import "errors"

func requireDedicatedProtection() error {
	return errors.New("dedicated service protection requires Linux")
}
