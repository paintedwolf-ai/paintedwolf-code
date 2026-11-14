//go:build !darwin && !linux

package denialprobe

import "errors"

var errUnsupported = errors.New("denial probe: no mount facility on this platform")

func unmount(string) error { return errUnsupported }

func mount(string) error { return errUnsupported }
