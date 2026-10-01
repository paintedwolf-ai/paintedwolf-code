//go:build !darwin

package browserengine

func clearMacQuarantine(path string) error {
	return nil
}
