package browser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// Timeline recording bounds.
const (
	MaxRecordWatch        = 8
	DefaultRecordTailMS   = 1000
	MaxRecordTailMS       = 5000
	maxTimelineFrames     = 300
	minFrameGapMS         = 33
	screencastJPEGQuality = 72
	recordFlushGrace      = 80 * time.Millisecond
	recordBinding         = "__lycaonRecord"
	// regionSampleInterval is how often the sampler asks whether the page's text may have moved.
	regionSampleInterval = 100 * time.Millisecond
	// regionSampleFloor bounds how long text geometry goes unsampled while the page reports
	// no change, so motion the page cannot report (a CSS animation) is still bracketed.
	regionSampleFloor = time.Second
)

// RecordOpts asks for a drive to be recorded as a timeline. Watch names elements whose
// geometry is sampled every frame; TailMS keeps recording after the last action.
type RecordOpts struct {
	Watch  []string `json:"watch,omitempty"`
	TailMS *int     `json:"tail_ms,omitempty"`
}

func (o RecordOpts) tail() time.Duration {
	ms := DefaultRecordTailMS
	if o.TailMS != nil {
		ms = *o.TailMS
	}
	return time.Duration(ms) * time.Millisecond
}

// ValidateRecordOpts checks recording options before a drive starts.
func ValidateRecordOpts(o RecordOpts) error {
	if len(o.Watch) > MaxRecordWatch {
		return browserengine.Reject("CAPTURE_RECORD_INVALID", map[string]any{"reason": "too_many_watch_selectors", "count": len(o.Watch), "max": MaxRecordWatch})
	}
	for _, sel := range o.Watch {
		if strings.TrimSpace(sel) == "" {
			return browserengine.Reject("CAPTURE_RECORD_INVALID", map[string]any{"reason": "empty_watch_selector"})
		}
	}
	if o.TailMS != nil && (*o.TailMS < 0 || *o.TailMS > MaxRecordTailMS) {
		return browserengine.Reject("CAPTURE_RECORD_INVALID", map[string]any{"reason": "bad_tail", "tail_ms": *o.TailMS, "max_tail_ms": MaxRecordTailMS})
	}
	return nil
}

type recordedFrame struct {
	wallMS float64
	jpeg   []byte
}

// regionSample is the page's text geometry at one moment. Samples with the same key carry
// the same geometry and share one screening.
type regionSample struct {
	wallMS  float64
	key     string
	regions PageRegions
}

// pageTelemetry is one event the in-page recorder streamed through the binding.
type pageTelemetry struct {
	wallMS float64
	kind   string
	body   map[string]any
}

// telemetryGeometry is the in-page recorder's report that text may have moved: a DOM
// mutation, a scroll, or a resize. It steers sampling and is not a timeline event.
const telemetryGeometry = "geometry"

// cdpSession is a CDP session on a page target that shares nothing with the page's own session.
type cdpSession struct {
	browser *rod.Browser
	id      proto.TargetSessionID
	ctx     context.Context
}

func (s *cdpSession) Call(ctx context.Context, sessionID, method string, params any) ([]byte, error) {
	return s.browser.Call(ctx, sessionID, method, params)
}

func (s *cdpSession) GetContext() context.Context { return s.ctx }

func (s *cdpSession) GetSessionID() proto.TargetSessionID { return s.id }

// recorderSession watches a page from a CDP session of its own, so a live preview's
// screencast on the page's main session runs undisturbed beside it.
type recorderSession struct {
	session  *cdpSession
	stop     context.CancelFunc
	listened chan struct{}
	scriptID proto.PageScriptIdentifier
	mu       sync.Mutex
	frames   []recordedFrame
	pending  *recordedFrame
	events   []pageTelemetry
	regions  []regionSample
	// geometryEpoch counts the page's reports that text may have moved.
	geometryEpoch int
	// collectRegions reads the page's text geometry; tests substitute a counter.
	collectRegions func(*rod.Page) PageRegions
}

func startRecorderSession(ctx context.Context, held *rod.Page, opts RecordOpts) (*recorderSession, error) {
	b := held.Browser()
	attached, err := proto.TargetAttachToTarget{TargetID: held.TargetID, Flatten: true}.Call(b.Context(ctx))
	if err != nil {
		return nil, fmt.Errorf("attach recorder: %w", err)
	}
	listenCtx, stop := context.WithCancel(ctx)
	page := &cdpSession{browser: b, id: attached.SessionID, ctx: ctx}
	s := &recorderSession{session: page, stop: stop, listened: make(chan struct{}), collectRegions: availablePageRegions}
	fail := func(err error) (*recorderSession, error) {
		s.detach(ctx)
		return nil, err
	}
	watch, err := surveyjson.Marshal(map[string]any{"binding": recordBinding, "watch": opts.Watch})
	if err != nil {
		return fail(err)
	}
	go s.listen(b.Context(listenCtx).Event())
	for _, call := range []func() error{
		func() error { return proto.RuntimeEnable{}.Call(page) },
		func() error { return proto.PageEnable{}.Call(page) },
		func() error { return proto.RuntimeAddBinding{Name: recordBinding}.Call(page) },
		func() error {
			// Recording continues into documents the drive navigates to.
			res, err := proto.PageAddScriptToEvaluateOnNewDocument{Source: `(() => {
  const opts = ` + string(watch) + `;
  const go = () => window.__lycaonDriver ? window.__lycaonDriver.startRecording(opts) : setTimeout(go, 0);
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", go, {once: true}); else go();
})()`}.Call(page)
			if err == nil {
				s.scriptID = res.Identifier
			}
			return err
		},
		func() error {
			vw, vh := viewportOf(held)
			return proto.PageStartScreencast{
				Format: proto.PageStartScreencastFormatJpeg, Quality: gsonInt(screencastJPEGQuality),
				MaxWidth: gsonInt(vw), MaxHeight: gsonInt(vh), EveryNthFrame: gsonInt(1),
			}.Call(page)
		},
	} {
		if err := call(); err != nil {
			return fail(fmt.Errorf("start recorder: %w", err))
		}
	}
	return s, nil
}

func gsonInt(v int) *int { return &v }

func viewportOf(page *rod.Page) (int, int) {
	metrics, err := proto.PageGetLayoutMetrics{}.Call(page)
	if err != nil || metrics.CSSLayoutViewport == nil {
		return DefaultViewportWidth, DefaultViewportHeight
	}
	return metrics.CSSLayoutViewport.ClientWidth, metrics.CSSLayoutViewport.ClientHeight
}

func (s *recorderSession) listen(events <-chan *rod.Message) {
	defer close(s.listened)
	for msg := range events {
		if msg.SessionID != s.session.id {
			continue
		}
		var frame proto.PageScreencastFrame
		var binding proto.RuntimeBindingCalled
		switch {
		case msg.Load(&frame):
			s.frame(&frame)
		case msg.Load(&binding):
			if binding.Name == recordBinding {
				s.telemetry(binding.Payload)
			}
		}
	}
}

// frame keeps at most one frame per minFrameGapMS; the newest frame inside a gap waits
// as pending so the last paint of a burst is never lost.
func (s *recorderSession) frame(e *proto.PageScreencastFrame) {
	_ = proto.PageScreencastFrameAck{SessionID: e.SessionID}.Call(s.session)
	if e.Metadata == nil || len(e.Data) == 0 {
		return
	}
	f := recordedFrame{wallMS: float64(e.Metadata.Timestamp) * 1000, jpeg: append([]byte(nil), e.Data...)}
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.frames); n > 0 && f.wallMS-s.frames[n-1].wallMS < minFrameGapMS {
		s.pending = &f
		return
	}
	s.frames = append(s.frames, f)
	s.pending = nil
}

func (s *recorderSession) telemetry(payload string) {
	var batch []map[string]any
	if json.Unmarshal([]byte(payload), &batch) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range batch {
		t, _ := item["t"].(float64)
		kind, _ := item["kind"].(string)
		if kind == telemetryGeometry {
			if epoch, ok := item["epoch"].(float64); ok {
				s.geometryEpoch = max(s.geometryEpoch, int(epoch))
			}
			continue
		}
		delete(item, "t")
		delete(item, "kind")
		s.events = append(s.events, pageTelemetry{wallMS: t, kind: kind, body: item})
	}
}

func (s *recorderSession) currentGeometryEpoch() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.geometryEpoch
}

// sampleRegions records the page's text geometry on the main session whenever the page
// reports that text may have moved, and at least every regionSampleFloor, so every frame
// can be screened against the samples taken on either side of it.
func (s *recorderSession) sampleRegions(ctx context.Context, page *rod.Page) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	take := func(p *rod.Page) {
		regions := s.collectRegions(p)
		sample := regionSample{wallMS: nowWallMS(), key: regionKey(regions), regions: regions}
		s.mu.Lock()
		s.regions = append(s.regions, sample)
		s.mu.Unlock()
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(regionSampleInterval)
		defer ticker.Stop()
		sampledEpoch := s.currentGeometryEpoch()
		sampledAt := time.Now()
		take(pageIn(page, ctx))
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				epoch := s.currentGeometryEpoch()
				if epoch == sampledEpoch && time.Since(sampledAt) < regionSampleFloor {
					continue
				}
				sampledEpoch, sampledAt = epoch, time.Now()
				take(pageIn(page, ctx))
			}
		}
	}()
	return func() {
		cancel()
		<-done
		// A closing sample covers the final frames.
		take(page)
	}
}

func pageIn(page *rod.Page, ctx context.Context) *rod.Page {
	if page == nil {
		return nil
	}
	return page.Context(ctx)
}

// regionKey identifies a sample's geometry, so unchanged text is screened once.
func regionKey(regions PageRegions) string {
	raw, err := surveyjson.Marshal(regions)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// finish stops the screencast and the listener, and returns what the session recorded.
func (s *recorderSession) finish() ([]recordedFrame, []pageTelemetry, []regionSample) {
	_ = proto.PageStopScreencast{}.Call(s.session)
	if s.scriptID != "" {
		_ = proto.PageRemoveScriptToEvaluateOnNewDocument{Identifier: s.scriptID}.Call(s.session)
	}
	_ = proto.RuntimeRemoveBinding{Name: recordBinding}.Call(s.session)
	s.stop()
	<-s.listened
	s.mu.Lock()
	defer s.mu.Unlock()
	frames := s.frames
	if s.pending != nil {
		frames = append(frames, *s.pending)
	}
	return frames, s.events, s.regions
}

func (s *recorderSession) detach(ctx context.Context) {
	s.stop()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pageCloseTimeout)
	defer cancel()
	_ = proto.TargetDetachFromTarget{SessionID: s.session.id}.Call(s.session.browser.Context(ctx))
}
