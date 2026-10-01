package evidence

import (
	"encoding/json"
	"strings"
)

// recallPayload contains the recall fields used by evidence capture.
type recallPayload struct {
	Hits []struct {
		Path    string   `json:"path"`
		URL     string   `json:"url"`
		Snippet string   `json:"snippet"`
		Body    []string `json:"body"`
	} `json:"hits"`
}

// populateRecallRecord captures recall text and indexes named paths and URLs.
func populateRecallRecord(projectDir string, rec *Record, content string) {
	if rec == nil {
		return
	}
	body := captureRecordBody(rec, content)
	var payload recallPayload
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return
	}
	for _, hit := range payload.Hits {
		if path := strings.TrimSpace(hit.Path); path != "" {
			addRecordPath(projectDir, rec, path, FidelityStructured)
		}
		if url := strings.TrimSpace(hit.URL); url != "" {
			rec.touchURL(url)
		}
	}
}
