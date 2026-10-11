package contractfixture

import (
	"testing"
)

func ReadSSEStreamFromPath(t *testing.T, baseURL, streamPath string) (string, bool) {
	t.Helper()
	return ReadSSEStream(t, baseURL+streamPath)
}
