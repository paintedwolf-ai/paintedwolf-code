package guard

import (
	"github.com/lycaon/lycaon/pkg/api"
)

// ProseFinishTurn is true on the last tool-loop iteration.
func ProseFinishTurn(sess *api.Session, iterIndex, maxIter int) bool {
	if sess == nil || maxIter <= 0 {
		return false
	}
	return iterIndex >= maxIter-1
}

// ProseTurn is a forced closeout or the last tool-loop iteration.
func ProseTurn(sess *api.Session, iterIndex, maxIter int, forced bool) bool {
	if forced {
		return true
	}
	return ProseFinishTurn(sess, iterIndex, maxIter)
}
