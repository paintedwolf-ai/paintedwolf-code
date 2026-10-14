//go:build darwin && !cgo

package hostpower

type platformInhibitor struct{}

func (platformInhibitor) Availability() error {
	return &UnavailableError{Reasons: []ProviderReason{{Provider: "iokit", Reason: "this build has no IOKit bridge"}}}
}

func (platformInhibitor) Acquire() (lease, error) {
	return nil, nil
}
