package webresearch

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/visualscreen"
	"github.com/lycaon/lycaon/pkg/api"
)

// handleFetchVisual screens image bytes before publishing a visual capture.
func handleFetchVisual(ctx context.Context, a fetchURLToolArgs, live fetchResponse) (string, *FetchURLResult, error) {
	ct := mediaTypeOnly(live.ContentType)
	if ct == "" {
		if strings.HasSuffix(strings.ToLower(live.URL), ".svg") {
			ct = "image/svg+xml"
		} else {
			ct = "image/png"
		}
	}
	rejectData := map[string]any{"url": live.URL, "path": live.URL, "format": strings.TrimPrefix(ct, "image/")}
	perceiveBytes, perceiveMime := live.Body, ct
	receipt := map[string]any{
		"url":          live.URL,
		"content_type": ct,
		"size_bytes":   len(live.Body),
		"mode":         "visual",
	}
	perceive := true
	if a.VisualScreen != nil {
		outcome, err := a.VisualScreen.Screen(ctx, visualscreen.VisualScreenInput{
			SessionID:     a.Tctx.Identity.SessionID,
			ProjectID:     a.Tctx.Identity.ProjectID,
			ToolName:      "fetch_url",
			ToolCallID:    a.Tctx.Identity.ToolCallID,
			Mime:          ct,
			RawBytes:      live.Body,
			PublicInbound: true,
		})
		if err != nil {
			if errors.Is(err, visualscreen.ErrVisualSecretWithheld) {
				return "", nil, &toolrejection.ToolReject{
					Code: "FETCH_URL_SECRET_WITHHELD",
					Data: map[string]any{"url": live.URL},
				}
			}
			return "", nil, fetchImageReject(err, rejectData)
		}
		if outcome.ExtractedText != "" {
			receipt["text"] = runeclamp.Clamp(outcome.ExtractedText, 4000)
		}
		if outcome.Perception != visualscreen.PerceptionOriginal {
			receipt["perception"] = string(outcome.Perception)
		}
		if outcome.Gap != "" {
			receipt["screening_gap"] = string(outcome.Gap)
		}
		perceiveBytes, perceiveMime, perceive = outcome.PerceiveBytes, outcome.Mime, outcome.PermitPerception()
	} else if kind, err := visualscreen.Classify(ct, live.Body); err != nil {
		return "", nil, fetchImageReject(err, rejectData)
	} else if kind == visualscreen.KindRaster {
		if _, err := visualscreen.CheckRasterBounds(live.Body); err != nil {
			return "", nil, fetchImageReject(err, rejectData)
		}
	}

	if perceive {
		if a.Tctx.Effects.Out == nil {
			a.Tctx.Effects.Out = &tools.ToolInvocationOut{}
		}
		a.Tctx.Effects.Out.Visual = &tools.VisualCapture{
			Mime:      perceiveMime,
			Bytes:     append([]byte(nil), perceiveBytes...),
			Source:    api.VisualArtifactSourceFetch,
			Caption:   live.URL,
			Perceive:  true,
			Projected: true,
		}
	}
	raw, _ := surveyjson.Marshal(receipt)
	noteRetrievedFrom(a.Tctx, live.URL)
	return string(raw), nil, nil
}

func fetchImageReject(err error, data map[string]any) error {
	var dims *visualscreen.DimensionsError
	if errors.As(err, &dims) {
		out := map[string]any{"width": dims.Width, "height": dims.Height, "max_dimension": dims.Max}
		for k, v := range data {
			out[k] = v
		}
		return &toolrejection.ToolReject{Code: "IMAGE_DIMENSIONS_EXCEEDED", Data: out}
	}
	if errors.Is(err, visualscreen.ErrImageUndecodable) {
		out := map[string]any{"rejection_reason": err.Error()}
		for k, v := range data {
			out[k] = v
		}
		return &toolrejection.ToolReject{Code: "IMAGE_CORRUPTED", Data: out}
	}
	return err
}
