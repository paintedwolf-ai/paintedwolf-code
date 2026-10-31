//go:build !unix

package referenceuserland

import (
	"errors"
	"io"
)

// ServeDriver answers one request inside a userland container, which is always Unix.
func ServeDriver(io.Reader, io.Writer) error {
	return errors.New("the reference userland driver runs only on Unix")
}
