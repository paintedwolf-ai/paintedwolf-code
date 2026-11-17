// Package archlint holds what every arch-lint check reports.
package archlint

// Finding is one defect, keyed by symbol so a change can be compared with its
// base regardless of line numbers.
type Finding struct {
	Kind    string `json:"kind"`
	Key     string `json:"key"`
	Pos     string `json:"pos"`
	Message string `json:"message"`
}
