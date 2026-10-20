//go:build !darwin && !linux

package confine

import "github.com/lycaon/lycaon/internal/osprocess"

const platformShebangBytes = 512
const maximumInterpreterDepth = 1

func platformShebang([]byte) ([]string, error) {
	return nil, osprocess.ErrExecutableObservationUnavailable
}
