//go:build windows

package exec

import "io"

// ProcessSubstitutionStream is an active <(list) or >(list) byte stream bound
// to a virtual descriptor path (/dev/fd/N).
type ProcessSubstitutionStream struct {
	Path   string
	Reader io.ReadCloser
	Writer io.WriteCloser
}

// BindProcessSubstitutions is a no-op on Windows.
func BindProcessSubstitutions(_ string, argv []string, _ []ProcessSubstitutionStream) ([]string, func(), error) {
	return argv, func() {}, nil
}
