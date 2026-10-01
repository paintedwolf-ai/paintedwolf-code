package sourceblob

import "os"

// Windows refuses replacing an existing read-only object.
func prepareObjectPublication(path string) (func(), error) {
	if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return func() { _ = os.Chmod(path, 0o400) }, nil
}
