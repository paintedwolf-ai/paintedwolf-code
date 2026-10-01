//go:build darwin || linux

package sourcecatalog

import "os"

func createStructuralTempFile(dir, pattern string) (*os.File, error) {
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	if err := os.Remove(file.Name()); err != nil { // #nosec G703 -- The name comes from the temporary file created above.
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
