package main

import (
	"context"
	"fmt"
	"io"
)

// The desktop owns stdin until shutdown. EOF also releases the root when the
// desktop crashes, while the next launch reclaims roots from a killed retainer.
func runNixRetainer(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	if _, err := fmt.Fprintln(stdout, "ready"); err != nil {
		return err
	}
	closed := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, stdin); close(closed) }()
	select {
	case <-closed:
		return nil
	case <-ctx.Done():
		return nil
	}
}
