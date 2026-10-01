package sourceledger

import (
	"encoding/json"

	"github.com/lycaon/lycaon/internal/sourcebranch"
)

func encodeRootBranches(roots map[string]sourcebranch.ID) (string, error) {
	if roots == nil {
		return "", nil
	}
	raw, err := json.Marshal(roots)
	return string(raw), err
}
