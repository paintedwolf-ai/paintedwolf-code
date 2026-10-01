package observability

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/lycaon/lycaon/internal/debugpaths"
)

const (
	httpDebugMaxEnv    = "LYCAON_HTTP_DEBUG_MAX_BYTES"
	defaultHTTPMaxBody = 32 * 1024
)

var jsonSecretFieldRE = buildJSONSecretFieldRE()

func buildJSONSecretFieldRE() *regexp.Regexp {
	alts := make([]string, 0, len(secretFieldNames))
	for _, name := range secretFieldNames {
		alts = append(alts, strings.ReplaceAll(regexp.QuoteMeta(name), "_", "[_-]?"))
	}
	return regexp.MustCompile(`(?i)"([^"]*(?:` + strings.Join(alts, "|") + `)[^"]*)"\s*:\s*"[^"]*"`)
}

// HTTPDebugEnabled reports whether inbound HTTP exchanges are mirrored to a debug log file.
func HTTPDebugEnabled() bool {
	return debugpaths.Enabled(debugpaths.KindHTTP)
}

type httpDebugEntry struct {
	Time          time.Time `json:"ts"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	DurationMs    int64     `json:"duration_ms,omitempty"`
	RequestID     string    `json:"request_id,omitempty"`
	Method        string    `json:"method"`
	Path          string    `json:"path"`
	Query         string    `json:"query,omitempty"`
	Status        int       `json:"status"`
	RequestBytes  int64     `json:"request_bytes,omitempty"`
	ResponseBytes int64     `json:"response_bytes,omitempty"`
	RequestBody   string    `json:"request_body,omitempty"`
	ResponseBody  string    `json:"response_body,omitempty"`
	Stream        bool      `json:"stream,omitempty"`
	Truncated     bool      `json:"truncated,omitempty"`
}

var (
	httpDebugOnce sync.Once
	httpDebug     *jsonlDebugLog
)

func initHTTPDebugLog() {
	if !HTTPDebugEnabled() {
		return
	}
	log, err := openJSONLDebugLog(true, debugpaths.KindHTTP)
	if err != nil {
		slog.Warn("http debug logging disabled", "err", err)
		return
	}
	httpDebug = log
	slog.Info("http debug logging enabled", "path", log.path)
}

func activeHTTPDebugLog() *jsonlDebugLog {
	httpDebugOnce.Do(initHTTPDebugLog)
	return httpDebug
}

func httpDebugMaxBody() int {
	raw := strings.TrimSpace(os.Getenv(httpDebugMaxEnv))
	if raw == "" {
		return defaultHTTPMaxBody
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultHTTPMaxBody
	}
	return n
}

func shouldSkipHTTPDebug(path string) bool {
	return path == "/health"
}

func isHTTPStreamPath(path string) bool {
	if path == "/v1/events" {
		return true
	}
	return strings.HasSuffix(path, "/stream")
}

const (
	invalidCredentialBody = "[REDACTED: credential-bearing body is not valid JSON]"
	oversizeRequestBody   = "[WITHHELD: request body exceeds the capture limit]"
	oversizeCaptureBody   = "[WITHHELD: response body exceeds the capture limit]"
	withheldResponseBody  = "[WITHHELD: response body carries a credential]"
)

// BodyCapturePredicate selects routes for a capture rule.
type BodyCapturePredicate func(method, path string) bool

// HTTPBodyCapturePolicy declares route-specific body rules.
type HTTPBodyCapturePolicy struct {
	CredentialRequest BodyCapturePredicate
	WithholdResponse  BodyCapturePredicate
}

// HTTPDebugMiddleware mirrors redacted exchanges to LYCAON_HTTP_DEBUG_FILE.
func HTTPDebugMiddleware(policy HTTPBodyCapturePolicy) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !HTTPDebugEnabled() || shouldSkipHTTPDebug(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			started := time.Now().UTC()
			maxBody := httpDebugMaxBody()
			credentialRequest := policy.CredentialRequest != nil &&
				policy.CredentialRequest(r.Method, r.URL.Path)
			requestCapture := captureHTTPRequestBody(r, maxBody)
			stream := isHTTPStreamPath(r.URL.Path)
			withholdResponse := policy.WithholdResponse != nil &&
				policy.WithholdResponse(r.Method, r.URL.Path)
			crw := newCaptureResponseWriter(w, maxBody, stream, withholdResponse)
			next.ServeHTTP(crw, r)
			requestCapture.finish()
			reqBody, reqBytes, reqTrunc := requestCapture.result(credentialRequest)
			logHTTPExchange(r, crw, reqBody, reqBytes, reqTrunc, started)
		})
	}
}

type captureRequestBody struct {
	io.ReadCloser
	body       bytes.Buffer
	captureMax int
	expected   int64
	read       int64
	truncated  bool
}

func captureHTTPRequestBody(r *http.Request, captureMax int) *captureRequestBody {
	capture := &captureRequestBody{captureMax: captureMax}
	if r.Body == nil || r.Method == http.MethodGet || r.Method == http.MethodHead {
		return capture
	}
	capture.ReadCloser = r.Body
	capture.expected = r.ContentLength
	capture.truncated = captureMax > 0 && r.ContentLength > int64(captureMax)
	r.Body = capture
	return capture
}

func (r *captureRequestBody) Read(p []byte) (int, error) {
	if r.ReadCloser == nil {
		return 0, io.EOF
	}
	n, err := r.ReadCloser.Read(p)
	r.read += int64(n)
	if n > 0 && !r.truncated && r.body.Len() < r.captureMax {
		remaining := r.captureMax - r.body.Len()
		captured := min(n, remaining)
		_, _ = r.body.Write(p[:captured])
	}
	if n > 0 && r.read > int64(r.captureMax) {
		r.truncated = true
	}
	return n, err
}

func (r *captureRequestBody) finish() {
	if r.ReadCloser == nil || r.truncated {
		return
	}
	_, _ = io.Copy(io.Discard, r)
}

func (r *captureRequestBody) result(credentialBearing bool) (body string, size int64, truncated bool) {
	size = max(r.read, r.expected)
	if size == 0 {
		return "", 0, false
	}
	if r.truncated {
		return oversizeRequestBody, size, true
	}
	redacted := redactHTTPBody(r.body.Bytes(), credentialBearing)
	body, truncated = truncateHTTPBody(redacted, r.captureMax)
	return body, size, truncated
}

// redactHTTPBody fully redacts bodies declared credential-bearing.
func redactHTTPBody(data []byte, credentialBearing bool) string {
	if len(data) == 0 {
		return ""
	}
	if credentialBearing {
		if !json.Valid(data) {
			return invalidCredentialBody
		}
		return RedactCaptureText(string(ScrubDeclaredCredentialJSON(data)))
	}
	if json.Valid(data) {
		return RedactCaptureJSON(data)
	}
	return RedactCaptureText(string(data))
}

func truncateHTTPBody(body string, logMax int) (string, bool) {
	if logMax <= 0 || len(body) <= logMax {
		return body, false
	}
	return body[:logMax], true
}

type captureResponseWriter struct {
	http.ResponseWriter
	status     int
	body       bytes.Buffer
	logMax     int
	captureMax int
	stream     bool
	withhold   bool
	truncated  bool
	written    int64
}

func newCaptureResponseWriter(inner http.ResponseWriter, maxBody int, stream, withhold bool) *captureResponseWriter {
	return &captureResponseWriter{
		ResponseWriter: inner,
		status:         http.StatusOK,
		logMax:         maxBody,
		captureMax:     maxBody,
		stream:         stream,
		withhold:       withhold,
	}
}

func (w *captureResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *captureResponseWriter) Write(b []byte) (int, error) {
	w.written += int64(len(b))
	if !w.stream && !w.withhold {
		if w.body.Len() < w.captureMax {
			remain := w.captureMax - w.body.Len()
			if len(b) > remain {
				_, _ = w.body.Write(b[:remain])
				w.truncated = true
			} else {
				_, _ = w.body.Write(b)
			}
		} else if len(b) > 0 {
			w.truncated = true
		}
	}
	return w.ResponseWriter.Write(b)
}

func (w *captureResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func logHTTPExchange(
	r *http.Request,
	w *captureResponseWriter,
	reqBody string,
	reqBytes int64,
	reqTrunc bool,
	started time.Time,
) {
	log := activeHTTPDebugLog()
	if log == nil {
		return
	}
	finished := time.Now().UTC()
	entry := httpDebugEntry{
		Time:          finished,
		StartedAt:     started,
		DurationMs:    finished.Sub(started).Milliseconds(),
		RequestID:     middleware.GetReqID(r.Context()),
		Method:        r.Method,
		Path:          r.URL.Path,
		Query:         RedactQueryForLog(r.URL.RawQuery),
		Status:        w.status,
		RequestBytes:  reqBytes,
		ResponseBytes: w.written,
		RequestBody:   reqBody,
		Stream:        w.stream,
	}
	if reqTrunc {
		entry.Truncated = true
	}
	if w.withhold {
		entry.ResponseBody = withheldResponseBody
	} else if !w.stream && w.body.Len() > 0 {
		if w.truncated {
			entry.ResponseBody = oversizeCaptureBody
			entry.Truncated = true
			log.write(entry)
			return
		}
		var redactedTruncated bool
		entry.ResponseBody, redactedTruncated = truncateHTTPBody(
			redactHTTPBody(w.body.Bytes(), false), w.logMax,
		)
		if redactedTruncated || w.logMax > 0 && w.body.Len() > w.logMax {
			entry.Truncated = true
		}
	}
	log.write(entry)
}
