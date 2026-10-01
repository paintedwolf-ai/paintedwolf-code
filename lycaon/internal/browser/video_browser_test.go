package browser

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"

	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/testutil"
)

// The fixtures are one second each of red, green, and blue at 320x180.
func videoFixture(t *testing.T, name, mime string) VideoMedia {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "video", name))
	testutil.FailErr(t, "read video fixture", err)
	return VideoMedia{MIME: mime, Bytes: raw}
}

// dominant names the strongest channel at a frame's center.
func dominant(t *testing.T, frame []byte) string {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(frame))
	testutil.FailErr(t, "decode frame", err)
	b := img.Bounds()
	r, g, bl, _ := img.At(b.Min.X+b.Dx()/2, b.Min.Y+b.Dy()/2).RGBA()
	switch {
	case r > g && r > bl:
		return "red"
	case g > r && g > bl:
		return "green"
	default:
		return "blue"
	}
}

func TestDecodeVideoDrawsTheFrameAtEachRequestedTime(t *testing.T) {
	pool := drivePool(t)
	for _, fx := range []struct{ name, mime string }{
		{"rgb.mp4", "video/mp4"},
		{"rgb.mov", "video/quicktime"},
		// Live-muxed like a browser recording: the container carries no duration.
		{"rgb-live.webm", "video/webm"},
	} {
		t.Run(fx.name, func(t *testing.T) {
			info, frames, err := pool.DecodeVideo(t.Context(), videoFixture(t, fx.name, fx.mime),
				VideoFrameRequest{TimesMS: []float64{500, 1500, 2500}, FrameWidth: 160})
			testutil.FailErr(t, "decode", err)
			if info.Width != 320 || info.Height != 180 || info.DurationMS < 2900 || info.DurationMS > 3100 {
				t.Fatalf("info = %+v", info)
			}
			want := []string{"red", "green", "blue"}
			if len(frames) != len(want) {
				t.Fatalf("frames = %d", len(frames))
			}
			for i, f := range frames {
				if got := dominant(t, f.JPEG); got != want[i] {
					t.Fatalf("frame at %v ms is %s, want %s", f.AtMS, got, want[i])
				}
				img, err := jpeg.Decode(bytes.NewReader(f.JPEG))
				testutil.FailErr(t, "decode frame", err)
				if img.Bounds().Dx() != 160 || img.Bounds().Dy() != 90 {
					t.Fatalf("frame size = %v, want 160x90", img.Bounds())
				}
			}
		})
	}
}

func TestDecodeVideoCropsToARegion(t *testing.T) {
	pool := drivePool(t)
	_, frames, err := pool.DecodeVideo(t.Context(), videoFixture(t, "rgb.mp4", "video/mp4"),
		VideoFrameRequest{TimesMS: []float64{1500}, Crop: &VideoCrop{X: 160, Y: 90, Width: 100, Height: 50}, FrameWidth: 960})
	testutil.FailErr(t, "decode crop", err)
	img, err := jpeg.Decode(bytes.NewReader(frames[0].JPEG))
	testutil.FailErr(t, "decode frame", err)
	// A crop is never enlarged past its own pixels.
	if img.Bounds().Dx() != 100 || img.Bounds().Dy() != 50 || dominant(t, frames[0].JPEG) != "green" {
		t.Fatalf("crop = %v", img.Bounds())
	}
	_, _, err = pool.DecodeVideo(t.Context(), videoFixture(t, "rgb.mp4", "video/mp4"),
		VideoFrameRequest{TimesMS: []float64{0}, Crop: &VideoCrop{X: 400, Y: 0, Width: 10, Height: 10}})
	var rej *browserengine.RejectError
	if !errors.As(err, &rej) || rej.Code != "VIDEO_WINDOW_INVALID" || rej.Data["reason"] != "crop_outside_video" || rej.Data["width"] != 320 {
		t.Fatalf("crop outside the video = %v", err)
	}
}

func TestDecodeVideoSpreadsFramesAcrossAWindow(t *testing.T) {
	pool := drivePool(t)
	media := videoFixture(t, "rgb.mp4", "video/mp4")
	_, frames, err := pool.DecodeVideo(t.Context(), media, VideoFrameRequest{Spread: 4, StartMS: 1000, EndMS: 2000, FrameWidth: 64})
	testutil.FailErr(t, "decode window", err)
	if len(frames) != 4 || frames[0].AtMS != 1125 || frames[3].AtMS != 1875 {
		t.Fatalf("window frames at %v", frameTimes(frames))
	}
	for _, f := range frames {
		if dominant(t, f.JPEG) != "green" {
			t.Fatalf("frame at %v left the green second", f.AtMS)
		}
	}
	_, _, err = pool.DecodeVideo(t.Context(), media, VideoFrameRequest{Spread: 2, StartMS: 5000})
	var rej *browserengine.RejectError
	if !errors.As(err, &rej) || rej.Code != "VIDEO_WINDOW_INVALID" || rej.Data["reason"] != "window_outside_video" {
		t.Fatalf("window past the end = %v", err)
	}
}

func frameTimes(frames []VideoFrame) []float64 {
	out := make([]float64, len(frames))
	for i, f := range frames {
		out[i] = f.AtMS
	}
	return out
}

func TestOverviewVideoSpreadsLabeledFramesAcrossTheWholeVideo(t *testing.T) {
	pool := drivePool(t)
	sheet, err := pool.OverviewVideo(t.Context(), videoFixture(t, "rgb.mp4", "video/mp4"))
	testutil.FailErr(t, "overview", err)
	if len(sheet.Frames) != videoOverviewFrames || sheet.Frames[0] > 200 || sheet.Frames[len(sheet.Frames)-1] < 2800 {
		t.Fatalf("frames at %v", sheet.Frames)
	}
	img, err := png.Decode(bytes.NewReader(sheet.Sheet))
	testutil.FailErr(t, "decode sheet", err)
	if img.Bounds().Dx() > 2048 || img.Bounds().Dx() <= img.Bounds().Dy() {
		t.Fatalf("sheet = %v", img.Bounds())
	}
}

func TestDecodeVideoRejectsWhatTheBrowserCannotPlay(t *testing.T) {
	pool := drivePool(t)
	for _, media := range []VideoMedia{
		// Chrome for Testing ships no HEVC decoder.
		videoFixture(t, "rgb-hevc.mp4", "video/mp4"),
		{MIME: "video/mp4", Bytes: []byte("\x00\x00\x00\x18ftypmp42 not a real movie")},
	} {
		_, _, err := pool.DecodeVideo(t.Context(), media, VideoFrameRequest{Spread: 2})
		var rej *browserengine.RejectError
		if !errors.As(err, &rej) || rej.Code != "VIDEO_UNDECODABLE" {
			t.Fatalf("undecodable video = %v", err)
		}
	}
	_, _, err := pool.DecodeVideo(t.Context(), videoFixture(t, "rgb.mp4", "video/mp4"), VideoFrameRequest{Spread: MaxVideoFrames + 1})
	var rej *browserengine.RejectError
	if !errors.As(err, &rej) || rej.Code != "VIDEO_FRAMES_BOUNDS" {
		t.Fatalf("too many frames = %v", err)
	}
}

func TestVideoRangeAnswersInChunks(t *testing.T) {
	const size = 10 << 20
	cases := []struct {
		header     string
		start, end int
	}{
		{"", 0, videoRangeChunk - 1},
		{"bytes=0-", 0, videoRangeChunk - 1},
		{"bytes=100-199", 100, 199},
		{"bytes=-1000", size - 1000, size - 1},
		{"bytes=9437184-", 9437184, size - 1},
	}
	for _, c := range cases {
		headers := proto.NetworkHeaders{}
		if c.header != "" {
			headers["Range"] = gson.New(c.header)
		}
		start, end := videoRange(headers, size)
		if start != c.start || end != c.end {
			t.Fatalf("%q = %d-%d, want %d-%d", c.header, start, end, c.start, c.end)
		}
	}
}

func TestSheetColumnsWidenForPortraitVideo(t *testing.T) {
	if got := sheetColumnsFor(12, image.NewGray(image.Rect(0, 0, 90, 160))); got != 6 {
		t.Fatalf("portrait columns = %d", got)
	}
	if got := sheetColumnsFor(12, image.NewGray(image.Rect(0, 0, 160, 90))); got != sheetColumns {
		t.Fatalf("landscape columns = %d", got)
	}
}
