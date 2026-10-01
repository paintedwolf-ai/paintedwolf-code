//go:build !darwin && !linux

package workspace

func copyExtendedMetadata(_, _ string) error {
	return nil
}
