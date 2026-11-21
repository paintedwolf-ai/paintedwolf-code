package timelinearchive_test

import (
	"bytes"
	"github.com/lycaon/lycaon/internal/timelinearchive"
	"github.com/lycaon/lycaon/internal/timelinearchive/timelinearchivetest"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPackUnpackAndPosterRoundTrip(t *testing.T) {
	m := timelinearchive.Manifest{
		DurationMS: 1200,
		Viewport:   timelinearchive.Size{Width: 800, Height: 600},
		Frames:     []timelinearchive.Frame{{AtMS: 0, File: timelinearchive.FrameFile(0), Width: 800, Height: 600}, {AtMS: 400, File: timelinearchive.FrameFile(1), Width: 800, Height: 600}},
		Actions:    []timelinearchive.Action{{Index: 0, Type: "click", Label: "click #go", StartMS: 10, EndMS: 90, OK: true}},
		Events:     []timelinearchive.Event{{AtMS: 120, Kind: "layout_shift", Detail: map[string]any{"value": 0.1}}},
		Summary:    timelinearchive.Summary{DurationMS: 1200, FrameCount: 2, Sheet: []timelinearchive.SheetCell{{AtMS: 0, Frame: 0, Why: "start"}}},
	}
	frames := [][]byte{[]byte("jpeg-0"), []byte("jpeg-1")}
	poster := []byte("\x89PNG poster")
	archive, err := timelinearchive.Pack(m, frames, poster)
	testutil.FailErr(t, "Pack failed", err)
	got, entries, err := timelinearchivetest.Unpack(archive)
	testutil.FailErr(t, "Unpack failed", err)
	if got.Version != timelinearchive.FormatVersion || got.DurationMS != 1200 || len(got.Frames) != 2 || got.Summary.Sheet[0].Why != "start" {
		t.Fatalf("manifest = %+v", got)
	}
	if !bytes.Equal(entries[timelinearchive.FrameFile(1)], frames[1]) {
		t.Fatalf("frame 1 = %q", entries[timelinearchive.FrameFile(1)])
	}
	gotPoster, err := timelinearchive.Poster(archive)
	if err != nil || !bytes.Equal(gotPoster, poster) {
		t.Fatalf("poster = %q, %v", gotPoster, err)
	}
}

func TestPackRefusesFramesThatDoNotMatchTheManifest(t *testing.T) {
	if _, err := timelinearchive.Pack(timelinearchive.Manifest{Frames: []timelinearchive.Frame{{File: timelinearchive.FrameFile(0)}}}, nil, nil); err == nil {
		t.Fatal("a manifest naming a missing frame was packed")
	}
	if _, err := timelinearchive.Poster([]byte("not a zip")); err == nil {
		t.Fatal("a non-archive yielded a poster")
	}
}
