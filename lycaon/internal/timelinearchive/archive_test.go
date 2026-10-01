package timelinearchive

import (
	"bytes"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPackUnpackAndPosterRoundTrip(t *testing.T) {
	m := Manifest{
		DurationMS: 1200,
		Viewport:   Size{Width: 800, Height: 600},
		Frames:     []Frame{{AtMS: 0, File: FrameFile(0), Width: 800, Height: 600}, {AtMS: 400, File: FrameFile(1), Width: 800, Height: 600}},
		Actions:    []Action{{Index: 0, Type: "click", Label: "click #go", StartMS: 10, EndMS: 90, OK: true}},
		Events:     []Event{{AtMS: 120, Kind: "layout_shift", Detail: map[string]any{"value": 0.1}}},
		Summary:    Summary{DurationMS: 1200, FrameCount: 2, Sheet: []SheetCell{{AtMS: 0, Frame: 0, Why: "start"}}},
	}
	frames := [][]byte{[]byte("jpeg-0"), []byte("jpeg-1")}
	poster := []byte("\x89PNG poster")
	archive, err := Pack(m, frames, poster)
	testutil.FailErr(t, "Pack failed", err)
	got, entries, err := Unpack(archive)
	testutil.FailErr(t, "Unpack failed", err)
	if got.Version != FormatVersion || got.DurationMS != 1200 || len(got.Frames) != 2 || got.Summary.Sheet[0].Why != "start" {
		t.Fatalf("manifest = %+v", got)
	}
	if !bytes.Equal(entries[FrameFile(1)], frames[1]) {
		t.Fatalf("frame 1 = %q", entries[FrameFile(1)])
	}
	gotPoster, err := Poster(archive)
	if err != nil || !bytes.Equal(gotPoster, poster) {
		t.Fatalf("poster = %q, %v", gotPoster, err)
	}
}

func TestPackRefusesFramesThatDoNotMatchTheManifest(t *testing.T) {
	if _, err := Pack(Manifest{Frames: []Frame{{File: FrameFile(0)}}}, nil, nil); err == nil {
		t.Fatal("a manifest naming a missing frame was packed")
	}
	if _, err := Poster([]byte("not a zip")); err == nil {
		t.Fatal("a non-archive yielded a poster")
	}
}
