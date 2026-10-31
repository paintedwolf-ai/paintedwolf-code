package evidence

import (
	"encoding/json"
	"regexp"

	"github.com/lycaon/lycaon/internal/sandbox"
)

var (
	scanFindingFileRE = regexp.MustCompile(`"file"\s*:\s*"([^"]+)"`)
	scanFindingURIRE  = regexp.MustCompile(`"uri"\s*:\s*"([^"]+)"`)
)

// populateScanArtifactRecord captures scan tool JSON and indexes finding paths.
func populateScanArtifactRecord(projectDir string, rec *Record, content string) {
	if rec == nil {
		return
	}
	body := captureRecordBody(rec, content)
	for _, p := range scanPathsFromArtifactJSON(body) {
		addRecordPath(projectDir, rec, p, FidelityStructured)
	}
}

// scanPathsFromArtifactJSON walks the real scan tool wire shapes (ScanQueryResponse,
// ScanPackToolResult, CodeScan, ListScansResponse) and returns repo-relative finding paths.
func scanPathsFromArtifactJSON(body string) []string {
	obj, ok := jsonToolPayload(body)
	if !ok {
		return scanJSONFilePaths(body)
	}
	seen := map[string]struct{}{}
	var out []string
	add := func(raw string) {
		p := NormalizeLedgerPath(raw)
		if p == "" || sandbox.HasParentTraversal(p) {
			return
		}
		if _, dup := seen[p]; dup {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	collectScanArtifactPaths(obj, add)
	return out
}

func collectScanArtifactPaths(obj map[string]any, add func(string)) {
	if obj == nil {
		return
	}
	collectScanGuidancePaths(obj["guidance"], add)
	collectScanGuidancePaths(obj["guidance_preview"], add)
	collectScanFindingPaths(obj["findings"], add)
	collectScanFindingPaths(obj["new_findings"], add)
	collectScanFindingPaths(obj["resolved_findings"], add)
	collectScanFindingPaths(obj["persisted_findings"], add)
	collectScanFindingPaths(obj["new_findings_sample"], add)
	collectScanGroupPaths(obj["groups"], add)
	if perScan, ok := obj["per_scan"].([]any); ok {
		for _, row := range perScan {
			if m, ok := row.(map[string]any); ok {
				collectScanGuidancePaths(m["guidance_preview"], add)
			}
		}
	}
	if scans, ok := obj["scans"].([]any); ok {
		for _, row := range scans {
			if m, ok := row.(map[string]any); ok {
				collectScanArtifactPaths(m, add)
			}
		}
	}
}

func collectScanGroupPaths(raw any, add func(string)) {
	if raw == nil {
		return
	}
	bytes, err := json.Marshal(raw)
	if err != nil {
		return
	}
	var groups []struct {
		Locations []struct {
			URI string `json:"uri"`
		} `json:"locations"`
	}
	if err := json.Unmarshal(bytes, &groups); err != nil {
		return
	}
	for _, g := range groups {
		for _, loc := range g.Locations {
			if loc.URI != "" {
				add(loc.URI)
			}
		}
	}
}

func collectScanGuidancePaths(raw any, add func(string)) {
	arr, ok := raw.([]any)
	if !ok {
		return
	}
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if f, ok := m["file"].(string); ok {
			add(f)
		}
	}
}

func collectScanFindingPaths(raw any, add func(string)) {
	arr, ok := raw.([]any)
	if !ok {
		return
	}
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		locs, ok := m["locations"].([]any)
		if !ok {
			continue
		}
		for _, loc := range locs {
			lm, ok := loc.(map[string]any)
			if !ok {
				continue
			}
			if uri, ok := lm["uri"].(string); ok {
				add(uri)
			}
		}
	}
}

func scanJSONFilePaths(body string) []string {
	var out []string
	seen := map[string]struct{}{}
	add := func(raw string) {
		p := NormalizeLedgerPath(raw)
		if p == "" || sandbox.HasParentTraversal(p) {
			return
		}
		if _, dup := seen[p]; dup {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	for _, m := range scanFindingFileRE.FindAllStringSubmatch(body, -1) {
		add(m[1])
	}
	for _, m := range scanFindingURIRE.FindAllStringSubmatch(body, -1) {
		add(m[1])
	}
	for _, m := range gitJSONPathRE.FindAllStringSubmatch(body, -1) {
		add(m[1])
	}
	return out
}
