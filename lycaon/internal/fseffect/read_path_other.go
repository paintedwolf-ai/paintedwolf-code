//go:build !darwin && !linux && !windows

package fseffect

import "os"

func (*ReadRoot) Canonical(string) (string, error)                    { return "", ErrUnsupported }
func (*ReadRoot) Readlink(string, func(string) error) (string, error) { return "", ErrUnsupported }

func (*ReadRoot) OpenExact(string) (*os.File, error) { return nil, ErrUnsupported }
