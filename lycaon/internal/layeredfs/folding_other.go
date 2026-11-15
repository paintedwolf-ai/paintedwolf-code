//go:build !darwin && !linux

package layeredfs

// ProbeNameFolding reports no folding where the platform offers no answer.
func ProbeNameFolding(string) NameFolding { return NameFolding{} }
