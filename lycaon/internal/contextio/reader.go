// Package contextio checks cancellation between chunks of synchronous file I/O.
package contextio

import (
	"context"
	"io"
)

type Reader struct {
	Context context.Context
	Source  io.Reader
	OnRead  func(int)
}

func (r Reader) Read(p []byte) (int, error) {
	if err := r.Context.Err(); err != nil {
		return 0, err
	}
	n, err := r.Source.Read(p)
	if n > 0 && r.OnRead != nil {
		r.OnRead(n)
	}
	return n, err
}
