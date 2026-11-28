package observability

import "sync"

type captureRedactorRegistration struct{ fn CaptureRedactor }

var (
	captureRedactorMu     sync.Mutex
	activeCaptureRedactor *captureRedactorRegistration
	debugClosersMu        sync.Mutex
	debugClosers          []func()
)

// RegisterDebugCloser registers a process-level debug capture closer invoked by CloseDebugCaptures.
func RegisterDebugCloser(fn func()) {
	if fn == nil {
		return
	}
	debugClosersMu.Lock()
	debugClosers = append(debugClosers, fn)
	debugClosersMu.Unlock()
}

// SetCaptureRedactor sets runtime catalog redaction until its owner releases it.
func SetCaptureRedactor(fn CaptureRedactor) func() {
	registration := &captureRedactorRegistration{fn: fn}
	captureRedactorMu.Lock()
	activeCaptureRedactor = registration
	captureRedactorMu.Unlock()
	return func() {
		captureRedactorMu.Lock()
		defer captureRedactorMu.Unlock()
		if activeCaptureRedactor == registration {
			activeCaptureRedactor = nil
		}
		registration.fn = nil
	}
}

// CloseHTTPDebug closes HTTP capture and clears its lazy state.
func CloseHTTPDebug() {
	httpDebug.close()
	httpDebugOnce = sync.Once{}
	httpDebug = nil
}

// CloseLLMDebug closes LLM capture and clears its state under the stream lock.
func CloseLLMDebug() {
	captureMu.Lock()
	defer captureMu.Unlock()
	llmCapture.close()
	sessionManifest.close()
	promptCacheObsLog.close()
	llmCaptureOnce = sync.Once{}
	llmCapture = nil
	sessionManifest = nil
	manifestSeen = sync.Map{}
	promptCacheObsOnce = sync.Once{}
	promptCacheObsLog = nil
	// Capture references are file-scoped.
	resetCaptureBodyRefs()
}

// CloseSSEDebug closes SSE capture and clears its lazy state.
func CloseSSEDebug() {
	sseDebug.close()
	sseDebugOnce = sync.Once{}
	sseDebug = nil
}

// CloseToolDebug closes tool capture and clears its lazy state.
func CloseToolDebug() {
	toolDebug.close()
	toolDebugOnce = sync.Once{}
	toolDebug = nil
}

// CloseWebSearchDebug closes web-search fetch capture and clears its lazy state.
func CloseWebSearchDebug() {
	webSearchDebug.close()
	webSearchDebugOnce = sync.Once{}
	webSearchDebug = nil
}

// CloseDenPerfDebug closes Den perf capture and clears its lazy state.
func CloseDenPerfDebug() {
	denPerfDebug.close()
	denPerfDebugOnce = sync.Once{}
	denPerfDebug = nil
}

// CloseDebugCaptures closes every process diagnostic file.
func CloseDebugCaptures() {
	CloseHTTPDebug()
	CloseLLMDebug()
	CloseSSEDebug()
	CloseToolDebug()
	CloseWebSearchDebug()
	CloseDenPerfDebug()
	CloseSummarizeDebug()
	closePerformance()
	debugClosersMu.Lock()
	closers := append(([]func())(nil), debugClosers...)
	debugClosersMu.Unlock()
	for _, fn := range closers {
		fn()
	}
	closeServeLogFile()
}
