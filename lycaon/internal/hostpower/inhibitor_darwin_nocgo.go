//go:build darwin && !cgo

package hostpower

type platformInhibitor struct{}

func (platformInhibitor) Supported() bool { return false }

func (platformInhibitor) Acquire() (lease, error) {
	return nil, nil
}
