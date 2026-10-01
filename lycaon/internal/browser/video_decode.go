package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/lycaon/lycaon/internal/browserengine"
)

// Video decode bounds.
const (
	MaxVideoFrames         = 24
	DefaultVideoFrameWidth = 960
	MaxVideoFrameWidth     = 2048
	// videoRangeChunk caps one answered byte range, so no single CDP message carries the whole file.
	videoRangeChunk  = 4 << 20
	videoOrigin      = "http://lycaon.video"
	videoDecodeLimit = 90 * time.Second
	videoFrameJPEG   = 0.86
)

// VideoMedia is a video's bytes and container type.
type VideoMedia struct {
	MIME  string
	Bytes []byte
}

// VideoInfo is what the browser reports about a decodable video.
type VideoInfo struct {
	DurationMS float64 `json:"duration_ms"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
}

// VideoCrop is a region of the video in its own pixels.
type VideoCrop struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// VideoFrameRequest names the frames to decode: times in ms, or Spread frames evenly across
// the window from StartMS to EndMS (zero EndMS is the end of the video); an optional crop;
// and the width each frame is scaled to.
type VideoFrameRequest struct {
	TimesMS    []float64
	Spread     int
	StartMS    float64
	EndMS      float64
	Crop       *VideoCrop
	FrameWidth int
}

// VideoFrame is one decoded frame, as JPEG.
type VideoFrame struct {
	AtMS float64
	JPEG []byte
}

// DecodeVideo loads a video in the managed browser and returns its facts and the frames at
// the requested times. The browser's own decoders decide what plays; a codec it lacks is
// reported as VIDEO_UNDECODABLE.
func (p *Pool) DecodeVideo(ctx context.Context, media VideoMedia, req VideoFrameRequest) (VideoInfo, []VideoFrame, error) {
	if n := max(len(req.TimesMS), req.Spread); n > MaxVideoFrames {
		return VideoInfo{}, nil, browserengine.Reject("VIDEO_FRAMES_BOUNDS", map[string]any{"count": n, "max": MaxVideoFrames})
	}
	width := req.FrameWidth
	if width <= 0 {
		width = DefaultVideoFrameWidth
	}
	width = min(width, MaxVideoFrameWidth)
	ctx, cancel := context.WithTimeout(ctx, videoDecodeLimit)
	defer cancel()
	page, err := p.NewPage(ctx, 640, 360)
	if err != nil {
		return VideoInfo{}, nil, err
	}
	defer func() { _ = closePage(ctx, page) }()
	page = page.Context(ctx)
	stop, err := serveVideo(ctx, page, media)
	if err != nil {
		return VideoInfo{}, nil, err
	}
	defer stop()
	if err := page.Navigate(videoOrigin + "/"); err != nil {
		return VideoInfo{}, nil, fmt.Errorf("open video page: %w", err)
	}
	crop := map[string]any(nil)
	if req.Crop != nil {
		crop = map[string]any{"x": req.Crop.X, "y": req.Crop.Y, "width": req.Crop.Width, "height": req.Crop.Height}
	}
	window := map[string]any{"start_ms": req.StartMS, "end_ms": req.EndMS}
	res, err := page.Eval(videoDecodeScript, req.TimesMS, req.Spread, window, crop, width, videoFrameJPEG)
	if err != nil {
		return VideoInfo{}, nil, fmt.Errorf("decode video: %w", err)
	}
	var out struct {
		Error  string    `json:"error"`
		Code   int       `json:"code"`
		Info   VideoInfo `json:"info"`
		Frames []struct {
			AtMS float64 `json:"at_ms"`
			Data string  `json:"data"`
		} `json:"frames"`
	}
	raw, err := res.Value.MarshalJSON()
	if err != nil {
		return VideoInfo{}, nil, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return VideoInfo{}, nil, fmt.Errorf("decode video result: %w", err)
	}
	switch out.Error {
	case "":
	case "crop_outside_video", "window_outside_video":
		return VideoInfo{}, nil, browserengine.Reject("VIDEO_WINDOW_INVALID", map[string]any{
			"reason": out.Error, "duration_ms": roundTenth(out.Info.DurationMS), "width": out.Info.Width, "height": out.Info.Height,
		})
	default:
		return VideoInfo{}, nil, browserengine.Reject("VIDEO_UNDECODABLE", map[string]any{"reason": out.Error, "media_error": out.Code, "mime": media.MIME})
	}
	frames := make([]VideoFrame, 0, len(out.Frames))
	for _, f := range out.Frames {
		jpeg, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(f.Data, "data:image/jpeg;base64,"))
		if err != nil {
			return VideoInfo{}, nil, fmt.Errorf("decode video frame: %w", err)
		}
		frames = append(frames, VideoFrame{AtMS: roundTenth(f.AtMS), JPEG: jpeg})
	}
	out.Info.DurationMS = roundTenth(out.Info.DurationMS)
	return out.Info, frames, nil
}

// videoDecodeScript loads the page's video, reports its facts, and draws each requested time.
const videoDecodeScript = `async (times, spread, window, crop, width, quality) => {
  const v = document.getElementById("v");
  const failure = () => ({error: v.error ? "media_error" : "no_video_track", code: v.error ? v.error.code : 0});
  const waitFor = (events, ready, start = () => {}) => new Promise((resolve) => {
    const settle = () => {
      if (!v.error && !ready()) return;
      for (const event of [...events, "error"]) v.removeEventListener(event, settle);
      resolve(!v.error);
    };
    for (const event of [...events, "error"]) v.addEventListener(event, settle);
    start();
    settle();
  });
  if (!await waitFor(["loadedmetadata"], () => v.readyState >= 1)) return failure();
  if (v.duration === Infinity) {
    // Seeking past the end discovers the duration of live-muxed recordings.
    if (!await waitFor(["durationchange", "seeked"], () => isFinite(v.duration), () => {
      v.currentTime = Number.MAX_SAFE_INTEGER;
    })) return failure();
  }
  if (!v.videoWidth || !v.videoHeight) return failure();
  if (!isFinite(v.duration)) return {error: "no_duration", code: 0};
  const info = {duration_ms: v.duration * 1000, width: v.videoWidth, height: v.videoHeight};
  if (spread > 0) {
    const lo = window.start_ms, hi = window.end_ms > 0 ? Math.min(window.end_ms, info.duration_ms) : info.duration_ms;
    if (lo >= info.duration_ms) return {error: "window_outside_video", code: 0, info};
    times = Array.from({length: spread}, (_, i) => lo + (i + 0.5) * (hi - lo) / spread);
  }
  times = times || [];
  const src = crop ? crop : {x: 0, y: 0, width: v.videoWidth, height: v.videoHeight};
  src.width = Math.min(src.width, v.videoWidth - src.x);
  src.height = Math.min(src.height, v.videoHeight - src.y);
  if (src.width <= 0 || src.height <= 0) return {error: "crop_outside_video", code: 0, info};
  const outW = Math.min(width, src.width);
  const canvas = document.createElement("canvas");
  canvas.width = outW;
  canvas.height = Math.max(1, Math.round(outW * src.height / src.width));
  const g = canvas.getContext("2d");
  const frames = [];
  for (const t of times) {
    const at = Math.min(Math.max(0, t / 1000), Math.max(0, v.duration - 0.001));
    if (!await waitFor(["seeked", "loadeddata"], () => !v.seeking && v.readyState >= 2, () => {
      v.currentTime = at;
    })) return failure();
    g.drawImage(v, src.x, src.y, src.width, src.height, 0, 0, canvas.width, canvas.height);
    frames.push({at_ms: at * 1000, data: canvas.toDataURL("image/jpeg", quality)});
  }
  return {info, frames};
}`

// serveVideo answers the page's requests on the video origin: the page, and the media in
// byte ranges. Nothing else the page asks for is reachable.
func serveVideo(ctx context.Context, page *rod.Page, media VideoMedia) (func(), error) {
	ctx, cancel := context.WithCancel(ctx)
	served := page.Context(ctx)
	enable := proto.FetchEnable{Patterns: []*proto.FetchRequestPattern{{URLPattern: "*", RequestStage: proto.FetchRequestStageRequest}}}
	if err := enable.Call(served); err != nil {
		cancel()
		return nil, fmt.Errorf("serve video: %w", err)
	}
	go served.EachEvent(func(e *proto.FetchRequestPaused) {
		answerVideoRequest(served, media, e)
	})()
	return cancel, nil
}

func answerVideoRequest(page *rod.Page, media VideoMedia, e *proto.FetchRequestPaused) {
	u, err := url.Parse(e.Request.URL)
	if err != nil || !SameOrigin(videoOrigin, e.Request.URL) {
		_ = proto.FetchFailRequest{RequestID: e.RequestID, ErrorReason: proto.NetworkErrorReasonBlockedByClient}.Call(page)
		return
	}
	fulfill := func(status int, headers map[string]string, body []byte) {
		entries := make([]*proto.FetchHeaderEntry, 0, len(headers))
		for k, v := range headers {
			entries = append(entries, &proto.FetchHeaderEntry{Name: k, Value: v})
		}
		_ = proto.FetchFulfillRequest{RequestID: e.RequestID, ResponseCode: status, ResponseHeaders: entries, Body: body}.Call(page)
	}
	switch u.Path {
	case "/":
		fulfill(http.StatusOK, map[string]string{"Content-Type": "text/html; charset=utf-8"},
			[]byte(`<!doctype html><video id="v" muted preload="auto" src="/media"></video>`))
	case "/media":
		start, end := videoRange(e.Request.Headers, len(media.Bytes))
		if start >= len(media.Bytes) {
			fulfill(http.StatusRequestedRangeNotSatisfiable, map[string]string{"Content-Range": "bytes */" + strconv.Itoa(len(media.Bytes))}, nil)
			return
		}
		fulfill(http.StatusPartialContent, map[string]string{
			"Content-Type":   servedVideoMIME(media.MIME),
			"Accept-Ranges":  "bytes",
			"Content-Range":  fmt.Sprintf("bytes %d-%d/%d", start, end, len(media.Bytes)),
			"Content-Length": strconv.Itoa(end - start + 1),
		}, media.Bytes[start:end+1])
	default:
		fulfill(http.StatusNotFound, nil, nil)
	}
}

// videoRange reads a request's byte range and caps it at one chunk. An absent or open-ended
// range starts a chunked read; the browser asks for the rest as it needs it.
func videoRange(headers proto.NetworkHeaders, size int) (int, int) {
	start, end := 0, size-1
	for k, v := range headers {
		if !strings.EqualFold(k, "Range") {
			continue
		}
		spec := strings.TrimPrefix(strings.TrimSpace(v.Str()), "bytes=")
		from, to, _ := strings.Cut(spec, "-")
		first, fromErr := strconv.Atoi(strings.TrimSpace(from))
		last, toErr := strconv.Atoi(strings.TrimSpace(to))
		switch {
		case fromErr != nil && toErr == nil:
			// A suffix range asks for the final bytes, where an MP4 often keeps its index.
			start = max(0, size-last)
		case fromErr == nil:
			start = first
			if toErr == nil && last < end {
				end = last
			}
		}
	}
	return start, min(end, start+videoRangeChunk-1)
}

// servedVideoMIME labels QuickTime as MP4: the two share the ISO media layout,
// and Chromium plays a QuickTime file only when it is served as MP4.
func servedVideoMIME(mime string) string {
	if mime == "video/quicktime" {
		return "video/mp4"
	}
	return mime
}
