//go:build !darwin && !linux && !windows

package fseffect

func (*ReadRoot) TestPath(string, string) (bool, error) { return false, ErrUnsupported }
