package commandprogram

import (
	"context"
	"io"
)

// NullInput is the typed native null endpoint. Path classification is
// supplied by the path adapter; it never opens an OS device path.
func NullInput() io.ReadCloser { return nullInput{} }

// IsNullInput reports whether r is the command program's null input endpoint.
func IsNullInput(r io.Reader) bool {
	_, ok := r.(nullInput)
	return ok
}

type nullInput struct{}

func (nullInput) InputReady() bool { return true }

func (nullInput) Read([]byte) (int, error) { return 0, io.EOF }
func (nullInput) Close() error             { return nil }

// borrowedInput leaves the descriptor lifetime with its enclosing scope.
type borrowedInput struct{ io.Reader }

func (b borrowedInput) Close() error { return nil }
func (b borrowedInput) ReadContext(ctx context.Context, data []byte) (int, error) {
	return readContext(ctx, b.Reader, data)
}
func (b borrowedInput) InputReady() bool { return inputReady(b.Reader) }
