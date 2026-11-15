//go:build !darwin

package fileclone

// CloneDirectory reports false: only APFS clones a directory tree in one call.
func CloneDirectory(string, string) (bool, error) { return false, nil }
