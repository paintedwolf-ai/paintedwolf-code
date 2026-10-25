package visualscreen

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"io"
	"os"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/ocrengine"
)

// Bounds on one recognition run. The raster is already held to the perception
// dimension bound, so a run that exceeds these is a stuck or runaway engine.
const (
	tesseractTimeout        = 30 * time.Second
	tesseractMaxOutputBytes = 8 << 20
)

// tesseractRequest is one recognition the runner performs.
type tesseractRequest struct {
	Engine         ocrengine.Engine
	PGM            []byte
	Timeout        time.Duration
	MaxOutputBytes int
}

// tesseractResult is what the engine process produced.
type tesseractResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// tesseractOCR recognizes text with the bundled Tesseract executable. The host
// decodes the raster and hands the engine a PGM on stdin; the engine answers
// with word geometry on stdout.
type tesseractOCR struct {
	resolve        func() (ocrengine.Engine, error)
	run            func(context.Context, tesseractRequest) (tesseractResult, error)
	timeout        time.Duration
	maxOutputBytes int
}

func newTesseractOCR(resolve func() (ocrengine.Engine, error)) *tesseractOCR {
	return &tesseractOCR{resolve: resolve, run: runTesseract, timeout: tesseractTimeout, maxOutputBytes: tesseractMaxOutputBytes}
}

func (t *tesseractOCR) RecognizeText(ctx context.Context, _ string, raw []byte) ([]TextSpan, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	engine, err := t.resolve()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOCRUnavailable, err)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decode raster: %w", err)
	}
	pgm, width, height := encodePGM(img, tesseractScale(img.Bounds()))
	res, err := t.run(ctx, tesseractRequest{Engine: engine, PGM: pgm, Timeout: t.timeout, MaxOutputBytes: t.maxOutputBytes})
	if err != nil {
		return nil, fmt.Errorf("tesseract: %w", err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("tesseract exited %d: %s", res.ExitCode, lastLine(res.Stderr))
	}
	spans, err := parseTesseractTSV(res.Stdout, width, height)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, lastLine(res.Stderr))
	}
	return spans, nil
}

// tesseractOrientationMargin is how far the best orientation score must lead
// the runner-up before the engine turns a block. The engine default of 7 never
// turns a capture of a few lines upside down; at 1 a two-line capture turned
// upside down reads, and upright rasters are not turned.
const tesseractOrientationMargin = "1"

// runTesseract executes the engine once under the host boundary: a private
// scratch directory as its only write root, no egress, a reduced environment,
// a deadline, and capped output. Orientation detection (page segmentation mode
// 1) turns rotated captures before reading and reports boxes in the raster's
// own coordinates; only the tsv renderer writes to stdout.
func runTesseract(ctx context.Context, req tesseractRequest) (tesseractResult, error) {
	if req.Engine.LanguageArgument == "" {
		return tesseractResult{}, fmt.Errorf("OCR engine has no verified recognition language profile")
	}
	scratch, err := os.MkdirTemp("", "pw-ocr-")
	if err != nil {
		return tesseractResult{}, fmt.Errorf("scratch: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	box, applied := confine.DefaultConfinement(confine.Request{Roots: []string{scratch}, Egress: confine.EgressDeny})
	if err := confine.RequireApplied(applied); err != nil {
		return tesseractResult{}, err
	}
	args := []string{"stdin", "stdout", "--psm", "1", "-l", req.Engine.LanguageArgument, "-c", "min_orientation_margin=" + tesseractOrientationMargin, "-c", "tessedit_create_tsv=1", "-c", "tessedit_create_txt=0"}
	stdout, stderr, code, err := execpkg.RunSeparate(ctx, req.Engine.Binary, args, execpkg.ExecOpts{
		Launch:         execpkg.OCREngineLaunch(box).WithReducedEnvironment(),
		Dir:            scratch,
		Timeout:        req.Timeout,
		MaxOutputBytes: req.MaxOutputBytes,
		Stdin:          &execpkg.StdinSpec{Reader: io.NopCloser(bytes.NewReader(req.PGM))},
		AppendEnv:      []string{"TESSDATA_PREFIX=" + req.Engine.TessdataDir, "OMP_THREAD_LIMIT=1"},
	})
	if err != nil {
		return tesseractResult{}, err
	}
	return tesseractResult{Stdout: stdout, Stderr: stderr, ExitCode: code}, nil
}

// lastLine is the engine's final diagnostic line, which names the failure.
func lastLine(stderr []byte) string {
	lines := strings.Split(strings.TrimSpace(string(stderr)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
