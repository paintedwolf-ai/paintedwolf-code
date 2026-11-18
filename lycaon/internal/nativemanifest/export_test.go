package nativemanifest

// MultiRootCapabilities returns the capability ids in order.
func MultiRootCapabilities() []string {
	out := make([]string, len(multiRootCapabilities))
	copy(out, multiRootCapabilities)
	return out
}
