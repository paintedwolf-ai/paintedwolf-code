package toolkit

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
)

// TruncationBanner marks incomplete survey output.
func TruncationBanner(summary string) string {
	if summary == "" {
		return ""
	}
	return fmt.Sprintf("--- TRUNCATED: %s ---\n", summary)
}

// OutlineBanner distinguishes an outline from literal content.
func OutlineBanner(summary string) string {
	if summary == "" {
		return ""
	}
	return fmt.Sprintf("--- OUTLINE: %s ---\n", summary)
}

// MarshalResponse attaches truncation metadata to a survey response.
func MarshalResponse(v any, banner string) (string, error) {
	raw, err := surveyjson.Marshal(v)
	if err != nil {
		return "", err
	}
	if banner == "" {
		return string(raw), nil
	}
	return patchResponseFields(raw, map[string]any{"truncation_banner": banner})
}

// PatchCoverage forces selected/total onto zoomed-out survey responses.
func PatchCoverage(raw string, selected, total int) (string, error) {
	if total <= 0 {
		return raw, nil
	}
	return patchResponseFields([]byte(raw), map[string]any{"selected": selected, "total": total})
}

func patchResponseFields(raw []byte, fields map[string]any) (string, error) {
	// Raw fields preserve numeric precision while metadata is added.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", err
	}
	if obj == nil {
		return "", fmt.Errorf("survey response must be a JSON object")
	}
	for name, value := range fields {
		encoded, err := surveyjson.Marshal(value)
		if err != nil {
			return "", err
		}
		obj[name] = encoded
	}
	patched, err := surveyjson.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(patched), nil
}

// AttachReceipt stamps a survey receipt onto a tool's encoded output.
func AttachReceipt(tool, path string, pathsTouched, bytesReturned int, truncated bool, output string) string {
	return surveyreceipt.Attach(output, surveyreceipt.New(tool, path, pathsTouched, bytesReturned, truncated))
}

// AppendNote separates nonempty notes with a semicolon.
func AppendNote(base, extra string) string {
	base = strings.TrimSpace(base)
	extra = strings.TrimSpace(extra)
	if extra == "" {
		return base
	}
	if base == "" {
		return extra
	}
	return base + "; " + extra
}
