package visual

import (
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
)

// StampArtifactID inserts the host-assigned ID first so compaction preserves it.
func StampArtifactID(content, artifactID string) string {
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" {
		return content
	}
	prefix, payload, suffix, ok := hostmarker.SplitToolJSONBody(content)
	if !ok {
		return stampArtifactIDLine(content, artifactID)
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(payload), &root); err != nil {
		quoted, qerr := json.Marshal(artifactID)
		if qerr != nil {
			return content
		}
		return prefix + `{"artifact_id":` + string(quoted) + `,` + payload[1:] + suffix
	}
	delete(root, "artifact_id")
	rest, err := json.Marshal(root)
	if err != nil {
		return content
	}
	quoted, err := json.Marshal(artifactID)
	if err != nil {
		return content
	}
	inner := strings.TrimSpace(string(rest))
	if inner == "{}" || inner == "null" {
		return prefix + `{"artifact_id":` + string(quoted) + `}` + suffix
	}
	return prefix + `{"artifact_id":` + string(quoted) + `,` + inner[1:] + suffix
}

func stampArtifactIDLine(content, artifactID string) string {
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "artifact_id:") {
		if i := strings.IndexByte(content, '\n'); i >= 0 {
			content = content[i+1:]
		} else {
			content = ""
		}
	}
	line := "artifact_id: " + artifactID
	if strings.TrimSpace(content) == "" {
		return line
	}
	return line + "\n" + content
}
