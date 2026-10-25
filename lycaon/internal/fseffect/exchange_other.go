//go:build !darwin && !linux

package fseffect

func ExchangeGuarded(string, string, string, string, string) error { return ErrUnsupported }
