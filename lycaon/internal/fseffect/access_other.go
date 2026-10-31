//go:build !darwin && !linux && !windows

package fseffect

func (*ReadRoot) Access(string, AccessMode, func(string) error) (bool, error) {
	return false, ErrUnsupported
}
