package browser

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/browserengine"
)

const (
	// CaptureModeScreenshot is the default single settled frame.
	CaptureModeScreenshot = "screenshot"
	// CaptureModeFilmstrip collects per-settle frames across a driven flow.
	CaptureModeFilmstrip = "filmstrip"
	// CaptureModeTimeline records the flow as it paints, with its events on one clock.
	CaptureModeTimeline = "timeline"
	// FilmstripMime is the store/wire mime for an ordered capture filmstrip container.
	FilmstripMime = "application/vnd.lycaon.filmstrip+zip"
)

// CaptureFrame is one settled shot inside a filmstrip (assert on State/Snapshot, not pixels).
type CaptureFrame struct {
	Index    int             `json:"index"`
	Caption  string          `json:"caption"`
	State    json.RawMessage `json:"state"`
	Snapshot json.RawMessage `json:"snapshot"`
	Mime     string          `json:"mime,omitempty"`
	Bytes    []byte          `json:"-"`
	// Coverage says whether this frame's text was fully readable for screening.
	Coverage *MaskCoverage `json:"coverage,omitempty"`
}

type filmstripManifest struct {
	Version int                      `json:"version"`
	Frames  []filmstripManifestFrame `json:"frames"`
}

type filmstripManifestFrame struct {
	Index   int    `json:"index"`
	File    string `json:"file"`
	Caption string `json:"caption"`
}

// NormalizeCaptureMode returns screenshot|filmstrip; empty defaults to screenshot.
func NormalizeCaptureMode(mode string) (string, error) {
	m := strings.ToLower(strings.TrimSpace(mode))
	switch m {
	case "", CaptureModeScreenshot:
		return CaptureModeScreenshot, nil
	case CaptureModeFilmstrip, CaptureModeTimeline:
		return m, nil
	default:
		return "", browserengine.Reject("CAPTURE_MODE_INVALID", map[string]any{"mode": mode})
	}
}

// PackFilmstripZip builds a store-backed filmstrip container (manifest + STORE PNG entries).
func PackFilmstripZip(frames []CaptureFrame) ([]byte, error) {
	if len(frames) == 0 {
		return nil, fmt.Errorf("filmstrip requires at least one frame")
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	manifest := filmstripManifest{Version: 1, Frames: make([]filmstripManifestFrame, 0, len(frames))}
	for _, fr := range frames {
		name := fmt.Sprintf("%03d.png", fr.Index)
		if len(fr.Bytes) == 0 {
			return nil, fmt.Errorf("filmstrip frame %d has empty png bytes", fr.Index)
		}
		w, err := zw.CreateHeader(&zip.FileHeader{
			Name:   name,
			Method: zip.Store,
		})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(fr.Bytes); err != nil {
			return nil, err
		}
		manifest.Frames = append(manifest.Frames, filmstripManifestFrame{
			Index: fr.Index, File: name, Caption: fr.Caption,
		})
	}
	manRaw, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	mw, err := zw.CreateHeader(&zip.FileHeader{Name: "manifest.json", Method: zip.Store})
	if err != nil {
		return nil, err
	}
	if _, err := mw.Write(manRaw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// actionCaption labels a filmstrip frame with the step that produced it.
func actionCaption(act CaptureAction) string {
	target := firstNonBlank(act.Selector, act.Testid, act.Label, act.Text, act.Role)
	typ := act.kind()
	with := func(detail string) string {
		if detail == "" {
			return typ
		}
		return typ + " " + detail
	}
	switch typ {
	case "":
		return "step"
	case "type", "fill":
		if target != "" {
			return with(target)
		}
		return with(strings.TrimSpace(act.Value))
	case "press":
		return with(strings.TrimSpace(act.Key))
	case "scroll":
		if act.By != nil {
			return with(fmt.Sprintf("%s by %g,%g", target, act.By.X, act.By.Y))
		}
		return with(target)
	case "drag":
		dest := ""
		if act.To != nil {
			dest = firstNonBlank(act.To.Selector, act.To.Testid, act.To.Label, act.To.Text, act.To.Role)
		} else if act.By != nil {
			dest = fmt.Sprintf("by %g,%g", act.By.X, act.By.Y)
		}
		return with(strings.TrimSpace(target + " → " + dest))
	case "route":
		return with(fmt.Sprintf("%d rules", len(act.Routes)))
	case "wait":
		return "wait idle"
	default:
		return with(target)
	}
}

func firstNonBlank(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
