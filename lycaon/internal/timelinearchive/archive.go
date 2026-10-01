// Package timelinearchive is the format of a recorded page timeline: screencast frames,
// the events and element geometry recorded on the same clock, the facts derived from them,
// and a poster contact sheet that stands in for the whole recording wherever one image is read.
package timelinearchive

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Mime identifies a timeline archive.
const Mime = "application/vnd.lycaon.timeline+zip"

// FormatVersion is the manifest version this package writes and reads.
const FormatVersion = 1

const (
	manifestEntry = "manifest.json"
	posterEntry   = "poster.png"
	// maxEntryBytes bounds one entry read back from an archive.
	maxEntryBytes = 32 << 20
)

// Size is a width and height in CSS pixels.
type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Box is an element's viewport box in CSS pixels.
type Box struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Frame is one painted frame, at a time relative to the recording start.
type Frame struct {
	AtMS   float64 `json:"at_ms"`
	File   string  `json:"file"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
}

// Action is one drive step and when it ran.
type Action struct {
	Index   int     `json:"index"`
	Type    string  `json:"type"`
	Label   string  `json:"label"`
	StartMS float64 `json:"start_ms"`
	EndMS   float64 `json:"end_ms"`
	OK      bool    `json:"ok"`
}

// Event is something the page did: a layout shift, a long task, a request, an error, or console output.
type Event struct {
	AtMS   float64        `json:"at_ms"`
	Kind   string         `json:"kind"`
	Detail map[string]any `json:"detail,omitempty"`
}

// WatchSample is a watched element's geometry whenever it changed.
type WatchSample struct {
	AtMS       float64  `json:"at_ms"`
	Present    bool     `json:"present"`
	Box        *Box     `json:"box,omitempty"`
	ScrollTop  *float64 `json:"scroll_top,omitempty"`
	ScrollLeft *float64 `json:"scroll_left,omitempty"`
}

// WatchTrack is one watched selector's samples in time order.
type WatchTrack struct {
	Selector string        `json:"selector"`
	Samples  []WatchSample `json:"samples"`
}

// Manifest describes a recording; frames are stored beside it as JPEG entries.
type Manifest struct {
	Version    int          `json:"version"`
	DurationMS float64      `json:"duration_ms"`
	Viewport   Size         `json:"viewport"`
	Frames     []Frame      `json:"frames"`
	Actions    []Action     `json:"actions"`
	Events     []Event      `json:"events"`
	Watch      []WatchTrack `json:"watch,omitempty"`
	Summary    Summary      `json:"summary"`
}

// FrameFile is the archive entry name for frame i.
func FrameFile(i int) string {
	return fmt.Sprintf("frames/%04d.jpg", i)
}

// Pack writes a manifest, its frames in manifest order, and the poster.
func Pack(m Manifest, frames [][]byte, poster []byte) ([]byte, error) {
	if len(frames) != len(m.Frames) {
		return nil, fmt.Errorf("timeline has %d frame entries and %d frame images", len(m.Frames), len(frames))
	}
	m.Version = FormatVersion
	manifest, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode timeline manifest: %w", err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// JPEG and PNG are already compressed; storing them keeps packing cheap.
	put := func(name string, body []byte, method uint16) error {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: method})
		if err != nil {
			return err
		}
		_, err = w.Write(body)
		return err
	}
	if err := put(manifestEntry, manifest, zip.Deflate); err != nil {
		return nil, err
	}
	if err := put(posterEntry, poster, zip.Store); err != nil {
		return nil, err
	}
	for i, f := range m.Frames {
		if err := put(f.File, frames[i], zip.Store); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Poster returns the contact sheet PNG a timeline perceives as.
func Poster(archive []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("open timeline archive: %w", err)
	}
	return readEntry(zr, posterEntry)
}

// Unpack reads the manifest and every entry of an archive.
func Unpack(archive []byte) (Manifest, map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("open timeline archive: %w", err)
	}
	raw, err := readEntry(zr, manifestEntry)
	if err != nil {
		return Manifest{}, nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, nil, fmt.Errorf("decode timeline manifest: %w", err)
	}
	if m.Version != FormatVersion {
		return Manifest{}, nil, fmt.Errorf("timeline manifest version %d is not %d", m.Version, FormatVersion)
	}
	entries := map[string][]byte{}
	for _, f := range zr.File {
		body, err := readFile(f)
		if err != nil {
			return Manifest{}, nil, err
		}
		entries[f.Name] = body
	}
	return m, entries, nil
}

func readEntry(zr *zip.Reader, name string) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name == name {
			return readFile(f)
		}
	}
	return nil, fmt.Errorf("timeline archive has no %s", name)
}

func readFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(io.LimitReader(rc, maxEntryBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxEntryBytes {
		return nil, errors.New("timeline archive entry exceeds its bound")
	}
	return body, nil
}
