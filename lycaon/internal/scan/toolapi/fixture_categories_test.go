package toolapi_test

import (
	"github.com/lycaon/lycaon/pkg/api"
)

func categoryOverlap(have, want []api.ScanCategory) bool {
	for _, w := range want {
		for _, h := range have {
			if h == w {
				return true
			}
		}
	}
	return false
}
