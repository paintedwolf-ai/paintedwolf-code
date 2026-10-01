//go:build !windows

package hostresources

func platformNamedPipeAvailable(string) (bool, error) { return false, nil }
