package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"sort"

	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/contactsheet"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/timelinearchive"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/visual"
)

// Recording is a drive recorded as a timeline: what the drive did, the archive a reader
// scrubs, and the facts derived from it.
type Recording struct {
	ActReport
	Archive  []byte
	Summary  timelinearchive.Summary
	Actions  []timelinearchive.Action
	Coverage *MaskCoverage
}

// Report is the recording's summary with its action spans, as drive results carry it.
func (r Recording) Report() *timelinearchive.Report {
	return &timelinearchive.Report{Summary: r.Summary, Actions: r.Actions}
}

// Recorded reports whether a recording exists: a failed step still returns the recording
// made up to and through it, beside the step's rejection.
func (r Recording) Recorded() bool {
	return len(r.Archive) > 0
}

// Record executes drive actions while recording page frames, layout shifts, requests,
// console logs, and errors. Failed steps return partial recordings up to the failure.
func (h *HeldPage) Record(ctx context.Context, actions []CaptureAction, opts RecordOpts, onAction func(CaptureAction, json.RawMessage)) (Recording, error) {
	if h == nil || h.Page == nil {
		return Recording{}, browserengine.Reject("BROWSER_UNAVAILABLE", map[string]any{"reason": "nil_page"})
	}
	if err := ValidateRecordOpts(opts); err != nil {
		return Recording{}, err
	}
	if err := validateActions(actions); err != nil {
		return Recording{}, err
	}
	ctx, cancel := pageOperationContext(ctx, h.Page, RasterizeTimeout+opts.tail())
	defer cancel()
	page := h.Page.Context(ctx)
	session, err := startRecorderSession(ctx, page, opts)
	if err != nil {
		return Recording{}, cdpUnavailable("recorder_session", err)
	}
	defer session.detach(ctx)
	mark := h.Evidence.mark()
	start := nowWallMS()
	if _, err := driverCall(page, "startRecording", map[string]any{"binding": recordBinding, "watch": opts.Watch}); err != nil {
		return Recording{}, fmt.Errorf("start page recorder: %w", err)
	}
	stopRegions := session.sampleRegions(ctx, page)
	var timed []timelinearchive.Action
	results, runErr := runActionBatch(actions, func(act CaptureAction) (json.RawMessage, error) {
		if act.Wait == "" {
			act.Wait = DefaultWaitIdle
		}
		began := nowWallMS()
		res, err := runAction(page, h.drive, act)
		timed = append(timed, timelinearchive.Action{
			Index: len(timed), Type: act.kind(), Label: actionCaption(act),
			StartMS: roundTenth(began - start), EndMS: roundTenth(nowWallMS() - start), OK: err == nil && driverOK(res),
		})
		return res, err
	}, func(act CaptureAction, res json.RawMessage) error {
		if onAction != nil {
			onAction(act, res)
		}
		return nil
	})
	// The tail runs after a failed step too: what the page did next is part of the answer.
	if err := pause(page, opts.tail()); err != nil && runErr == nil {
		runErr = err
	}
	_, _ = driverCall(page, "stopRecording", nil)
	_ = pause(page, recordFlushGrace)
	stopRegions()
	frames, telemetry, regions := session.finish()
	end := nowWallMS()
	if ctx.Err() != nil {
		return Recording{}, runErr
	}
	console, network, errs := h.Evidence.recordedSince(mark)
	built, err := buildTimeline(ctx, h, timelineInput{
		startWallMS: start, endWallMS: end, frames: frames, telemetry: telemetry, regions: regions,
		console: console, network: network, errors: errs, actions: timed, watch: opts.Watch,
	})
	if err != nil {
		return Recording{}, errors.Join(runErr, err)
	}
	evidence, err := projectEvidence(ctx, h.Projector, h.CaptureScope, h.Evidence.since(mark))
	if err != nil {
		return Recording{}, errors.Join(runErr, err)
	}
	built.ActReport = ActReport{Results: results, Evidence: evidence, RoutesActive: h.drive.routes.len()}
	return built, runErr
}

type timelineInput struct {
	startWallMS, endWallMS float64
	frames                 []recordedFrame
	telemetry              []pageTelemetry
	regions                []regionSample
	console                []timedLine
	network                []NetworkRecord
	errors                 []PageError
	actions                []timelinearchive.Action
	watch                  []string
}

func (in timelineInput) at(wallMS float64) float64 {
	return roundTenth(max(0, wallMS-in.startWallMS))
}

// screenedFrame is a frame after secret screening, with how much of it changed since the one before.
type screenedFrame struct {
	atMS     float64
	jpeg     []byte
	image    image.Image
	changed  float64
	coverage bool
}

func buildTimeline(ctx context.Context, h *HeldPage, in timelineInput) (Recording, error) {
	frames := framesInWindow(in.frames, in.startWallMS, in.endWallMS)
	if len(frames) == 0 {
		return Recording{}, browserengine.Reject("CAPTURE_RECORD_EMPTY", map[string]any{"reason": "no_frames"})
	}
	frames = thinFrames(frames, maxTimelineFrames)
	screened, err := screenFrames(ctx, h, in, frames)
	if err != nil {
		return Recording{}, err
	}
	manifest := timelinearchive.Manifest{
		DurationMS: roundTenth(in.endWallMS - in.startWallMS),
		Viewport:   timelinearchive.Size{Width: h.Width, Height: h.Height},
		Actions:    in.actions,
		Events:     timelineEvents(in),
		Watch:      watchTracks(in),
	}
	manifest.Summary = summarize(manifest, screened)
	manifest, err = screenManifest(ctx, h, manifest)
	if err != nil {
		return Recording{}, err
	}
	poster, err := composePoster(screened, manifest)
	if err != nil {
		return Recording{}, err
	}
	archive, manifest, err := packWithinBudget(manifest, screened, poster)
	if err != nil {
		return Recording{}, err
	}
	coverage := &MaskCoverage{Structured: true}
	for _, f := range screened {
		coverage.Structured = coverage.Structured && f.coverage
	}
	return Recording{Archive: archive, Summary: manifest.Summary, Actions: manifest.Actions, Coverage: coverage}, nil
}

// framesInWindow keeps the last frame painted before the recording started, as its first
// frame, and every frame painted until it stopped.
func framesInWindow(frames []recordedFrame, start, end float64) []recordedFrame {
	sort.SliceStable(frames, func(i, j int) bool { return frames[i].wallMS < frames[j].wallMS })
	out := make([]recordedFrame, 0, len(frames))
	for _, f := range frames {
		if f.wallMS > end {
			break
		}
		if f.wallMS <= start && len(out) > 0 {
			out[0] = f
			continue
		}
		out = append(out, f)
	}
	return out
}

// thinFrames keeps an evenly spread subset, always with the first and last frame.
func thinFrames(frames []recordedFrame, limit int) []recordedFrame {
	if len(frames) <= limit {
		return frames
	}
	out := make([]recordedFrame, 0, limit)
	for i := range limit {
		out = append(out, frames[i*(len(frames)-1)/(limit-1)])
	}
	return out
}

// screenFrames masks every frame with the screened text geometry sampled just before and
// just after it was painted, and measures how much each frame changed. Samples with the
// same geometry share one screening.
func screenFrames(ctx context.Context, h *HeldPage, in timelineInput, frames []recordedFrame) ([]screenedFrame, error) {
	if h.Projector == nil {
		return nil, captureprojection.ErrUnavailable
	}
	masks := make([]captureprojection.RasterMask, len(in.regions))
	screened := map[string]captureprojection.RasterMask{}
	for i, sample := range in.regions {
		if mask, ok := screened[sample.key]; ok && sample.key != "" {
			masks[i] = mask
			continue
		}
		mask, err := h.Projector.ScreenRegions(ctx, h.CaptureScope, float64(h.Width), float64(h.Height), sample.regions.Regions, sample.regions.Complete)
		if err != nil {
			return nil, fmt.Errorf("screen recorded page text: %w", err)
		}
		masks[i] = mask
		screened[sample.key] = mask
	}
	out := make([]screenedFrame, 0, len(frames))
	var previous *luma
	for _, f := range frames {
		mask, ok := bracketingMask(in.regions, masks, f.wallMS)
		if !ok {
			return nil, browserengine.Reject("CAPTURE_RECORD_EMPTY", map[string]any{"reason": "no_screening_geometry"})
		}
		safe, err := mask.Apply("image/jpeg", f.jpeg)
		if err != nil {
			return nil, err
		}
		img, err := jpeg.Decode(bytes.NewReader(safe))
		if err != nil {
			return nil, fmt.Errorf("decode recorded frame: %w", err)
		}
		grid := lumaGrid(img)
		sf := screenedFrame{atMS: in.at(f.wallMS), jpeg: safe, image: img, coverage: mask.Metadata.StructuredCoverage}
		if previous != nil {
			sf.changed = grid.changedFraction(previous)
		}
		previous = grid
		out = append(out, sf)
	}
	return out, nil
}

// bracketingMask unions the masks from the samples on either side of a frame, so text that
// moved between samples stays covered wherever it was painted.
func bracketingMask(samples []regionSample, masks []captureprojection.RasterMask, wallMS float64) (captureprojection.RasterMask, bool) {
	if len(samples) == 0 {
		return captureprojection.RasterMask{}, false
	}
	after := sort.Search(len(samples), func(i int) bool { return samples[i].wallMS >= wallMS })
	switch {
	case after == 0:
		return masks[0], true
	case after == len(samples):
		return masks[len(masks)-1], true
	default:
		return masks[after-1].Union(masks[after]), true
	}
}

// screenManifest screens every text the manifest carries: labels, URLs, console output,
// error messages, and the names of elements that shifted.
func screenManifest(ctx context.Context, h *HeldPage, m timelinearchive.Manifest) (timelinearchive.Manifest, error) {
	raw, err := surveyjson.Marshal(m)
	if err != nil {
		return m, err
	}
	safe, _, err := h.Projector.JSON(ctx, h.CaptureScope, "capture.browser.timeline", raw)
	if err != nil {
		return m, fmt.Errorf("screen timeline: %w", err)
	}
	var out timelinearchive.Manifest
	if err := json.Unmarshal(safe, &out); err != nil {
		return m, fmt.Errorf("decode screened timeline: %w", err)
	}
	return out, nil
}

// composePoster renders the contact sheet the timeline perceives as.
func composePoster(frames []screenedFrame, m timelinearchive.Manifest) ([]byte, error) {
	cells := make([]contactsheet.Cell, 0, len(m.Summary.Sheet))
	for _, c := range m.Summary.Sheet {
		cells = append(cells, contactsheet.Cell{Image: frames[c.Frame].image, Label: fmt.Sprintf("%.2fs · %s", c.AtMS/1000, c.Why)})
	}
	return contactsheet.Compose(cells, contactsheet.Options{MaxEdge: providerwire.MaxImageDimension, Columns: sheetColumns})
}

// packWithinBudget drops every other frame not shown on the sheet until the archive fits,
// and renumbers the sheet's frames to the ones the archive keeps.
func packWithinBudget(m timelinearchive.Manifest, frames []screenedFrame, poster []byte) ([]byte, timelinearchive.Manifest, error) {
	keep := make([]bool, len(frames))
	for i := range keep {
		keep[i] = true
	}
	pinned := map[int]bool{0: true, len(frames) - 1: true}
	for _, c := range m.Summary.Sheet {
		pinned[c.Frame] = true
	}
	sheet := append([]timelinearchive.SheetCell(nil), m.Summary.Sheet...)
	for {
		m.Frames = m.Frames[:0]
		var images [][]byte
		renumbered := map[int]int{}
		for i, f := range frames {
			if !keep[i] {
				continue
			}
			renumbered[i] = len(images)
			m.Frames = append(m.Frames, timelinearchive.Frame{
				AtMS: f.atMS, File: timelinearchive.FrameFile(len(images)),
				Width: f.image.Bounds().Dx(), Height: f.image.Bounds().Dy(),
			})
			images = append(images, f.jpeg)
		}
		for i, c := range sheet {
			m.Summary.Sheet[i].Frame = renumbered[c.Frame]
		}
		m.Summary.FrameCount = len(images)
		archive, err := timelinearchive.Pack(m, images, poster)
		if err != nil {
			return nil, m, err
		}
		if len(archive) <= visual.MaxFrameArchiveBytes {
			return archive, m, nil
		}
		dropped := false
		odd := false
		for i := range frames {
			if !keep[i] || pinned[i] {
				continue
			}
			if odd {
				keep[i] = false
				dropped = true
			}
			odd = !odd
		}
		if !dropped {
			return nil, m, browserengine.Reject("CAPTURE_OUTPUT_OVERSIZED", map[string]any{
				"bytes": len(archive), "max_bytes": visual.MaxFrameArchiveBytes, "capture_output_kind": "timeline archive",
			})
		}
	}
}
