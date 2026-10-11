package contractfixture

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func MustJSON(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	testutil.FailErr(t, "marshal JSON string", err)
	return raw
}
