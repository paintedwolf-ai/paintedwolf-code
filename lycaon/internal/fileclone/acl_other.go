//go:build !darwin

package fileclone

// CopyACL does nothing here: Linux ACLs are extended attributes and travel with them.
func CopyACL(string, string) error { return nil }
