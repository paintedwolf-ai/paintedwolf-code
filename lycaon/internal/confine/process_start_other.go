//go:build !darwin && !linux

package confine

import "github.com/lycaon/lycaon/internal/osprocess"

func nativeArgumentBudget() (int, error) { return 0, osprocess.ErrExecutableObservationUnavailable }
