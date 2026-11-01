//go:build darwin && !cgo

package socketref

func volumeTemporaryDirectory(string) (string, error) { return "", ErrUnsupported }
