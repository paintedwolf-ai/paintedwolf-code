package catalog

import (
	"errors"
	"fmt"
)

// ErrInvalidScanEngineSelection reports an explicit scanner that is not selected for the project.
type ErrInvalidScanEngineSelection struct {
	ScannerID string
	Reason    string
}

// ErrNoScannerForCategories reports an empty structured registry selection.
var ErrNoScannerForCategories = errors.New("no scanner for categories")

func (e *ErrInvalidScanEngineSelection) Error() string {
	return fmt.Sprintf("scanner %q cannot satisfy request: %s", e.ScannerID, e.Reason)
}
