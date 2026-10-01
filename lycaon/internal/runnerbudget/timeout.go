// Package runnerbudget reads budget settings shared by verification runners.
package runnerbudget

import (
	"os"
	"strconv"
)

const TimeoutScaleEnv = "PW_TEST_TIMEOUT_SCALE"

// TimeoutScale returns the runner's bounded scheduler allowance.
func TimeoutScale() int {
	scale, err := strconv.Atoi(os.Getenv(TimeoutScaleEnv))
	if err != nil || scale < 1 || scale > 4 {
		return 1
	}
	return scale
}
