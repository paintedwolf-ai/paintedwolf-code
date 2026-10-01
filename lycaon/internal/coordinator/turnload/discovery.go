package turnload

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// DiscoveryPageSize bounds each catalog response independently of catalog size.
const DiscoveryPageSize = 20

// DiscoveryEntry describes an available identifier without loading it.
type DiscoveryEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Discovery is a catalog page for selection by the calling model.
type Discovery struct {
	Status      string           `json:"status"`
	Failure     RankingFailure   `json:"failure,omitempty"`
	Entries     []DiscoveryEntry `json:"entries"`
	Total       int              `json:"total"`
	NextNeed    string           `json:"next_need,omitempty"`
	Instruction string           `json:"instruction"`
}

type discoveryCursor struct {
	Need   string `json:"need"`
	Digest string `json:"digest"`
	Offset int    `json:"offset"`
}

// Discover binds pagination to the need, surface scope, and current catalog.
func Discover(scope, need, cursor, status string, failure RankingFailure, entries []DiscoveryEntry) (*Discovery, error) {
	entries = append([]DiscoveryEntry{}, entries...)
	for i := range entries {
		entries[i].Description = BoundDescription(entries[i].Description)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	raw, err := json.Marshal(struct {
		Scope   string
		Need    string
		Entries []DiscoveryEntry
	}{scope, need, entries})
	if err != nil {
		return nil, err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	offset := 0
	if cursor != "" {
		if len(cursor) > 65536 {
			return nil, fmt.Errorf("discovery continuation exceeds its length limit")
		}
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		var next discoveryCursor
		if err != nil || json.Unmarshal(raw, &next) != nil || next.Need != need || next.Digest != digest || next.Offset <= 0 || next.Offset >= len(entries) {
			return nil, fmt.Errorf("discovery continuation is invalid or does not match the current request and catalog")
		}
		offset = next.Offset
	}
	end := min(offset+DiscoveryPageSize, len(entries))
	out := &Discovery{Status: status, Failure: failure, Entries: entries[offset:end], Total: len(entries), Instruction: "Select a relevant entry by calling this tool with its exact name as need. To see more entries, pass next_need unchanged as need. To restart discovery, repeat the original description."}
	if end < len(entries) {
		raw, err := json.Marshal(discoveryCursor{Need: need, Digest: digest, Offset: end})
		if err != nil {
			return nil, err
		}
		out.NextNeed = "discovery:" + base64.RawURLEncoding.EncodeToString(raw)
	}
	return out, nil
}

// ParseDiscoveryNeed recognizes the continuation supplied only in fallback results.
func ParseDiscoveryNeed(need string) (string, string, error) {
	cursor, continuation := strings.CutPrefix(need, "discovery:")
	if !continuation {
		return need, "", nil
	}
	if len(cursor) > 65536 {
		return "", "", fmt.Errorf("discovery continuation exceeds its length limit")
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	var next discoveryCursor
	if err != nil || json.Unmarshal(raw, &next) != nil || next.Need == "" || next.Offset <= 0 || next.Digest == "" {
		return "", "", fmt.Errorf("discovery continuation is invalid")
	}
	return next.Need, cursor, nil
}
