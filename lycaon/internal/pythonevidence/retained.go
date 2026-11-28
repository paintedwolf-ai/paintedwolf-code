package pythonevidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/tooloutput"
)

var (
	// ErrArtifactNotFound is returned when an artifact ID does not exist.
	ErrArtifactNotFound = errors.New("retained artifact not found")
	// ErrArtifactStoreFull is returned when storing an artifact would exceed
	// configured count or aggregate byte bounds.
	ErrArtifactStoreFull = errors.New("retained artifact store capacity exceeded")
	// ErrMissingRetainedStreamPort is returned by production constructors when
	// no durable retained-stream backing store is supplied.
	ErrMissingRetainedStreamPort = errors.New("production retained artifact store requires a durable RetainedStreamPort")
)

// ProducerLanguage identifies which execution engine produced a retained
// artifact for cross-language handoff without round-tripping through model context.
type ProducerLanguage string

const (
	ProducerPython  ProducerLanguage = "python"
	ProducerCommand ProducerLanguage = "command"
)

// RetainedStreamPort bridges PY05 retained artifacts onto Painted Wolf's
// durable tooloutput.RetainedStreamReference storage seam.
type RetainedStreamPort interface {
	StoreScreened(ctx context.Context, artifactID string, content tooloutput.ScreenedOutput) (tooloutput.RetainedStreamReference, error)
	LoadScreened(ctx context.Context, ref tooloutput.RetainedStreamReference) ([]byte, error)
	ReleaseStream(ctx context.Context, ref tooloutput.RetainedStreamReference) error
}

// RetainedArtifactSummary is the compact metadata handle safe for model
// feedback without inlining large payloads into model context.
type RetainedArtifactSummary struct {
	ArtifactID        string                              `json:"artifact_id"`
	SessionID         string                              `json:"session_id"`
	CheckoutID        string                              `json:"checkout_id,omitempty"`
	Actor             string                              `json:"actor"`
	View              string                              `json:"view"`
	ExecutionID       string                              `json:"execution_id"`
	ParentOperationID string                              `json:"parent_operation_id,omitempty"`
	Producer          ProducerLanguage                    `json:"producer"`
	MediaType         string                              `json:"media_type"`
	ByteLength        int64                               `json:"byte_length"`
	SHA256            string                              `json:"sha256"`
	RedactedCount     int                                 `json:"redacted_count,omitempty"`
	Truncated         bool                                `json:"truncated,omitempty"`
	StreamRef         *tooloutput.RetainedStreamReference `json:"stream_ref,omitempty"`
	CreatedAt         time.Time                           `json:"created_at"`
}

// RetainedArtifact holds a bounded, secret-screened derived payload exchanged
// between Portable Python and Portable Command.
type RetainedArtifact struct {
	RetainedArtifactSummary
	Data []byte `json:"-"`
}

// RetainedArtifactConfig bounds a RetainedArtifactStore.
type RetainedArtifactConfig struct {
	MaxArtifactBytes int64
	MaxTotalBytes    int64
	MaxArtifacts     int
	Screener         SecretScreener
	StreamPort       RetainedStreamPort
	Now              func() time.Time
}

// RetainedArtifactStore stores bounded, screened derived artifacts so Python
// and Portable Command can exchange data without round-tripping large payloads
// through model context. Every read enforces ViewerScope authorization.
type RetainedArtifactStore struct {
	mu               sync.Mutex
	maxArtifactBytes int64
	maxTotalBytes    int64
	maxArtifacts     int
	screener         SecretScreener
	streamPort       RetainedStreamPort
	now              func() time.Time
	artifacts        map[string]RetainedArtifact
	totalBytes       int64
	seq              uint64
}

const (
	defaultMaxArtifactBytes int64 = 256 * 1024
	defaultMaxTotalBytes    int64 = 2 * 1024 * 1024
	defaultMaxArtifacts           = 64
)

// NewProductionRetainedArtifactStore constructs a fail-closed production
// RetainedArtifactStore requiring both an active host SecretScreener and a
// durable RetainedStreamPort.
func NewProductionRetainedArtifactStore(cfg RetainedArtifactConfig) (*RetainedArtifactStore, error) {
	if cfg.Screener == nil {
		return nil, ErrMissingSecretScreener
	}
	if cfg.StreamPort == nil {
		return nil, ErrMissingRetainedStreamPort
	}
	return newRetainedArtifactStore(cfg), nil
}

// NewTestRetainedArtifactStore constructs an in-memory RetainedArtifactStore
// for unit tests and offline fixtures.
func NewTestRetainedArtifactStore(cfg RetainedArtifactConfig) *RetainedArtifactStore {
	if cfg.Screener == nil {
		cfg.Screener = TestSecretScreener{}
	}
	return newRetainedArtifactStore(cfg)
}

func newRetainedArtifactStore(cfg RetainedArtifactConfig) *RetainedArtifactStore {
	if cfg.MaxArtifactBytes <= 0 {
		cfg.MaxArtifactBytes = defaultMaxArtifactBytes
	}
	if cfg.MaxTotalBytes <= 0 {
		cfg.MaxTotalBytes = defaultMaxTotalBytes
	}
	if cfg.MaxArtifacts <= 0 {
		cfg.MaxArtifacts = defaultMaxArtifacts
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &RetainedArtifactStore{
		maxArtifactBytes: cfg.MaxArtifactBytes,
		maxTotalBytes:    cfg.MaxTotalBytes,
		maxArtifacts:     cfg.MaxArtifacts,
		screener:         cfg.Screener,
		streamPort:       cfg.StreamPort,
		now:              cfg.Now,
		artifacts:        make(map[string]RetainedArtifact),
	}
}

// PutRequest describes a derived payload to screen and retain.
type PutRequest struct {
	SessionID         string
	CheckoutID        string
	Actor             string
	View              string
	ExecutionID       string
	ParentOperationID string
	Producer          ProducerLanguage
	MediaType         string
	Data              []byte
}

// Put screens req.Data through tooloutput.ScreenedOutput, enforces per-artifact
// and aggregate store bounds, persists to StreamPort if configured, and returns
// the compact RetainedArtifactSummary handle.
func (s *RetainedArtifactStore) Put(ctx context.Context, req PutRequest) (RetainedArtifactSummary, error) {
	if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.Actor) == "" || strings.TrimSpace(req.View) == "" {
		return RetainedArtifactSummary{}, ErrIncompleteScope
	}
	if strings.TrimSpace(req.ExecutionID) == "" {
		return RetainedArtifactSummary{}, errors.New("retained artifact requires execution_id")
	}
	if req.Producer != ProducerPython && req.Producer != ProducerCommand {
		return RetainedArtifactSummary{}, fmt.Errorf("unsupported artifact producer %q", req.Producer)
	}
	mediaType := strings.TrimSpace(req.MediaType)
	if mediaType == "" {
		mediaType = "text/plain"
	}

	screenedOut, redactions := ScreenToOutput(s.screener, string(req.Data))
	payload := []byte(screenedOut.String())
	truncated := false
	if int64(len(payload)) > s.maxArtifactBytes {
		payload = payload[:s.maxArtifactBytes]
		truncated = true
		screenedOut = tooloutput.Screened(string(payload))
	}

	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])

	s.mu.Lock()
	if len(s.artifacts)+1 > s.maxArtifacts {
		s.mu.Unlock()
		return RetainedArtifactSummary{}, fmt.Errorf("%w: artifact count %d > %d", ErrArtifactStoreFull, len(s.artifacts)+1, s.maxArtifacts)
	}
	if s.totalBytes+int64(len(payload)) > s.maxTotalBytes {
		s.mu.Unlock()
		return RetainedArtifactSummary{}, fmt.Errorf("%w: total bytes %d > %d", ErrArtifactStoreFull, s.totalBytes+int64(len(payload)), s.maxTotalBytes)
	}
	s.seq++
	id := fmt.Sprintf("art-%d", s.seq)
	s.mu.Unlock()

	var streamRef *tooloutput.RetainedStreamReference
	if s.streamPort != nil {
		ref, err := s.streamPort.StoreScreened(ctx, id, screenedOut)
		if err != nil {
			return RetainedArtifactSummary{}, fmt.Errorf("store retained stream: %w", err)
		}
		streamRef = &ref
	}

	summary := RetainedArtifactSummary{
		ArtifactID:        id,
		SessionID:         req.SessionID,
		CheckoutID:        req.CheckoutID,
		Actor:             req.Actor,
		View:              req.View,
		ExecutionID:       req.ExecutionID,
		ParentOperationID: req.ParentOperationID,
		Producer:          req.Producer,
		MediaType:         mediaType,
		ByteLength:        int64(len(payload)),
		SHA256:            digest,
		RedactedCount:     redactions,
		Truncated:         truncated,
		StreamRef:         streamRef,
		CreatedAt:         s.now().UTC(),
	}

	var stored []byte
	if s.streamPort == nil {
		stored = make([]byte, len(payload))
		copy(stored, payload)
	}

	s.mu.Lock()
	s.artifacts[id] = RetainedArtifact{
		RetainedArtifactSummary: summary,
		Data:                    stored,
	}
	s.totalBytes += summary.ByteLength
	s.mu.Unlock()
	return summary, nil
}

// Get authorizes scope against the artifact's owning session/checkout/actor/view
// before returning a copy of the artifact. Fails closed on incomplete scope or
// cross-session/cross-worker access.
func (s *RetainedArtifactStore) Get(ctx context.Context, scope ViewerScope, artifactID string) (RetainedArtifact, error) {
	if err := ValidateViewerScope(scope); err != nil {
		return RetainedArtifact{}, err
	}
	s.mu.Lock()
	art, ok := s.artifacts[artifactID]
	s.mu.Unlock()
	if !ok {
		return RetainedArtifact{}, ErrArtifactNotFound
	}
	owner := ExecutionRecord{
		SessionID:  art.SessionID,
		CheckoutID: art.CheckoutID,
		Actor:      art.Actor,
		View:       art.View,
	}
	if !AuthorizeExecutionAccess(owner, scope) {
		return RetainedArtifact{}, ErrAccessDenied
	}
	if s.streamPort != nil && art.StreamRef != nil {
		data, err := s.streamPort.LoadScreened(ctx, *art.StreamRef)
		if err != nil {
			return RetainedArtifact{}, fmt.Errorf("load retained stream: %w", err)
		}
		art.Data = data
		return art, nil
	}
	copied := make([]byte, len(art.Data))
	copy(copied, art.Data)
	art.Data = copied
	return art, nil
}

// Delete removes an artifact and reclaims its byte budget.
func (s *RetainedArtifactStore) Delete(ctx context.Context, artifactID string) bool {
	s.mu.Lock()
	art, ok := s.artifacts[artifactID]
	if !ok {
		s.mu.Unlock()
		return false
	}
	delete(s.artifacts, artifactID)
	s.totalBytes -= art.ByteLength
	s.mu.Unlock()
	if s.streamPort != nil && art.StreamRef != nil {
		_ = s.streamPort.ReleaseStream(ctx, *art.StreamRef)
	}
	return true
}

// ListByExecution returns summaries for authorized artifacts belonging to
// executionID, sorted deterministically by ArtifactID.
func (s *RetainedArtifactStore) ListByExecution(scope ViewerScope, executionID string) ([]RetainedArtifactSummary, error) {
	if err := ValidateViewerScope(scope); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []RetainedArtifactSummary
	for _, art := range s.artifacts {
		if art.ExecutionID != executionID {
			continue
		}
		owner := ExecutionRecord{
			SessionID:  art.SessionID,
			CheckoutID: art.CheckoutID,
			Actor:      art.Actor,
			View:       art.View,
		}
		if !AuthorizeExecutionAccess(owner, scope) {
			continue
		}
		out = append(out, art.RetainedArtifactSummary)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ArtifactID < out[j].ArtifactID
	})
	return out, nil
}

// TotalBytes returns the aggregate retained byte count across the store.
func (s *RetainedArtifactStore) TotalBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.totalBytes
}
