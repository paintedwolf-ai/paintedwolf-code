//go:build !linux && !darwin

package observability

// PeakResidentBytes is unavailable here and reads -1.
func PeakResidentBytes() int64 { return -1 }
