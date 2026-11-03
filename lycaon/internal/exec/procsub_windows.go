//go:build windows

package exec

import "io"

// ProcessSubstitutionStream is an active <(list) or >(list) byte stream to be
// materialized as a named pipe (FIFO) for a confined external stage.
type ProcessSubstitutionStream struct {
	Reader io.ReadCloser
	Writer io.WriteCloser
}

// BindProcessSubstitutions is a no-op on Windows.
func BindProcessSubstitutions(_ string, _ []ProcessSubstitutionStream) ([]string, func(), error) {
	return nil, func() {}, nil
}
