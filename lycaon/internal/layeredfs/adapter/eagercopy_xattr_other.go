//go:build !darwin && !linux

package adapter

import (
	"errors"
	"fmt"
)

func readPathXattrs(p string) (map[string][]byte, error) {
	if readPathXattrsHook != nil {
		if err := readPathXattrsHook(p); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func writePathXattrs(p string, xattrs map[string][]byte) error {
	if len(xattrs) == 0 {
		return nil
	}
	return fmt.Errorf("write xattrs on %q: %w", p, errors.ErrUnsupported)
}
