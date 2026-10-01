package evidence

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
)

type captureFilmstripPayload struct {
	Capture string `json:"capture"`
	Mime    string `json:"mime"`
	Frames  []struct {
		Index          int             `json:"index"`
		Caption        string          `json:"caption"`
		State          json.RawMessage `json:"state"`
		Snapshot       json.RawMessage `json:"snapshot"`
		EvidenceHandle string          `json:"evidence_handle"`
	} `json:"frames"`
}

// CaptureFrameHighlightRecords creates one snapshot record per filmstrip frame.
// FrameIndex is one-based so zero means absent.
func CaptureFrameHighlightRecords(content string) []Record {
	payload, ok := parseCaptureFilmstrip(content)
	if !ok || len(payload.Frames) == 0 {
		return nil
	}
	out := make([]Record, 0, len(payload.Frames))
	for _, fr := range payload.Frames {
		frameBody := struct {
			Index    int             `json:"index"`
			Caption  string          `json:"caption"`
			State    json.RawMessage `json:"state"`
			Snapshot json.RawMessage `json:"snapshot"`
		}{
			Index: fr.Index, Caption: fr.Caption,
			State: rawOrEmpty(fr.State), Snapshot: rawOrEmpty(fr.Snapshot),
		}
		raw, err := json.Marshal(frameBody)
		if err != nil {
			continue
		}
		rec := Record{
			Kind:       "page",
			Shape:      ShapeSurfaceSnapshot,
			SourceTool: "capture_page",
			Fidelity:   FidelityStructured,
			Surface:    SurfaceDOM,
			FrameIndex: fr.Index + 1,
			Survey:     ActiveBinding().IsSurveyKind("page"),
		}
		captureRecordBody(&rec, string(raw))
		out = append(out, rec)
	}
	return out
}

// PatchCaptureFrameHandles writes minted page# handles onto frames[].evidence_handle.
func PatchCaptureFrameHandles(content string, handles []string) (string, error) {
	prefix, payload, suffix, ok := hostmarker.SplitToolJSONBody(content)
	if !ok {
		return content, fmt.Errorf("capture filmstrip json not found")
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(payload), &root); err != nil {
		return content, err
	}
	rawFrames, ok := root["frames"].([]any)
	if !ok || len(rawFrames) == 0 {
		return content, fmt.Errorf("frames missing")
	}
	if len(handles) != len(rawFrames) {
		return content, fmt.Errorf("handle count %d != frame count %d", len(handles), len(rawFrames))
	}
	for i, item := range rawFrames {
		frame, ok := item.(map[string]any)
		if !ok {
			return content, fmt.Errorf("frame %d not an object", i)
		}
		frame["evidence_handle"] = handles[i]
		rawFrames[i] = frame
	}
	root["frames"] = rawFrames
	out, err := json.Marshal(root)
	if err != nil {
		return content, err
	}
	return prefix + string(out) + suffix, nil
}

func parseCaptureFilmstrip(content string) (captureFilmstripPayload, bool) {
	_, payload, _, ok := hostmarker.SplitToolJSONBody(content)
	if !ok {
		return captureFilmstripPayload{}, false
	}
	var parsed captureFilmstripPayload
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return captureFilmstripPayload{}, false
	}
	if len(parsed.Frames) == 0 {
		return captureFilmstripPayload{}, false
	}
	mode := strings.ToLower(strings.TrimSpace(parsed.Capture))
	mime := strings.ToLower(strings.TrimSpace(parsed.Mime))
	if mode != "filmstrip" && !strings.Contains(mime, "filmstrip") {
		return captureFilmstripPayload{}, false
	}
	return parsed, true
}

func rawOrEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	return raw
}
