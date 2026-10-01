package bedrock

import (
	"encoding/base64"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	testutil.FailErr(t, "base64.StdEncoding.DecodeString failed", err)
	return png
}
