package backup

import (
	_ "embed"
	"encoding/json"
)

//go:embed archive-limits.json
var archiveLimitsJSON []byte

type archiveLimitContract struct {
	CompressedBytes    int64  `json:"compressed_bytes"`
	ExpandedBytes      uint64 `json:"expanded_bytes"`
	ManifestBytes      int    `json:"manifest_bytes"`
	Entries            int    `json:"entries"`
	SymlinkTargetBytes int64  `json:"symlink_target_bytes"`
}

var archiveLimits = func() archiveLimitContract {
	var limits archiveLimitContract
	if err := json.Unmarshal(archiveLimitsJSON, &limits); err != nil {
		panic(err)
	}
	if limits.CompressedBytes <= 0 || limits.ExpandedBytes < uint64(limits.CompressedBytes) || limits.ManifestBytes <= 0 || limits.Entries < 2 || limits.SymlinkTargetBytes <= 0 {
		panic("invalid embedded backup limits")
	}
	return limits
}()
