package pythonevidence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	// ErrArtifactNotFound is returned when an artifact ID does not exist.
	ErrArtifactNotFound = errors.New("retained artifact not found")
	// ErrArtifactStoreFull is returned when storing an artifact would exceed
	// configured count or aggregate byte bounds.
	ErrArtifactStoreFull = errors.New("retained artifact store capacity exceeded")
)

// ProducerLanguage identifies which execution engine produced a retained
// artifact for cross-language handoff without round-tripping through model context.
type ProducerLanguage string

const (
	ProducerPython  ProducerLanguage = "python"
	ProducerCommand ProducerLanguage = "command"
)

// RetainedArtifactSummary is the compact metadata handle safe for model
// feedback without inlining large payloads into model context.
type RetainedArtifactSummary struct {
	ArtifactID        string           `json:"artifact_id"`
	ExecutionID       string           `json:"execution_id"`
	ParentOperationID string           `json:"parent_operation_id,omitempty"`
	Producer          ProducerLanguage `json:"producer"`
	MediaType         string           `json:"media_type"`
	ByteLength        int64            `json:"byte_length"`
	SHA256            string           `json:"sha256"`
	RedactedCount     int              `json:"redacted_count,omitempty"`
	Truncated         bool             `json:"truncated,omitempty"`
	CreatedAt         time.Time        `json:"created_at"`
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
	Now              func() time.Time
}

// RetainedArtifactStore stores bounded, screened derived artifacts so Python
// and Portable Command can exchange data without round-tripping large payloads
// through model context.
type RetainedArtifactStore struct {
	mu               sync.Mutex
	maxArtifactBytes int64
	maxTotalBytes    int64
	maxArtifacts     int
	screener         SecretScreener
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

// NewRetainedArtifactStore constructs a bounded RetainedArtifactStore.
func NewRetainedArtifactStore(cfg RetainedArtifactConfig) *RetainedArtifactStore {
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
		screener:         effectiveScreener(cfg.Screener),
		now:              cfg.Now,
		artifacts:        make(map[string]RetainedArtifact),
	}
}

// PutRequest describes a derived payload to screen and retain.
type PutRequest struct {
	ExecutionID       string
	ParentOperationID string
	Producer          ProducerLanguage
	MediaType         string
	Data              []byte
}

// Put screens req.Data, enforces per-artifact and aggregate store bounds, and
// returns the compact RetainedArtifactSummary handle.
func (s *RetainedArtifactStore) Put(req PutRequest) (RetainedArtifactSummary, error) {
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

	screenedText, redactions := s.screener.ScreenText(string(req.Data))
	payload := []byte(screenedText)
	truncated := false
	if int64(len(payload)) > s.maxArtifactBytes {
		payload = payload[:s.maxArtifactBytes]
		truncated = true
	}

	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.artifacts)+1 > s.maxArtifacts {
		return RetainedArtifactSummary{}, fmt.Errorf("%w: artifact count %d > %d", ErrArtifactStoreFull, len(s.artifacts)+1, s.maxArtifacts)
	}
	if s.totalBytes+int64(len(payload)) > s.maxTotalBytes {
		return RetainedArtifactSummary{}, fmt.Errorf("%w: total bytes %d > %d", ErrArtifactStoreFull, s.totalBytes+int64(len(payload)), s.maxTotalBytes)
	}

	s.seq++
	id := fmt.Sprintf("art-%d", s.seq)
	summary := RetainedArtifactSummary{
		ArtifactID:        id,
		ExecutionID:       req.ExecutionID,
		ParentOperationID: req.ParentOperationID,
		Producer:          req.Producer,
		MediaType:         mediaType,
		ByteLength:        int64(len(payload)),
		SHA256:            digest,
		RedactedCount:     redactions,
		Truncated:         truncated,
		CreatedAt:         s.now().UTC(),
	}
	stored := make([]byte, len(payload))
	copy(stored, payload)
	s.artifacts[id] = RetainedArtifact{
		RetainedArtifactSummary: summary,
		Data:                    stored,
	}
	s.totalBytes += int64(len(stored))
	return summary, nil
}

// Get retrieves a copy of a retained artifact by ID.
func (s *RetainedArtifactStore) Get(artifactID string) (RetainedArtifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	art, ok := s.artifacts[artifactID]
	if !ok {
		return RetainedArtifact{}, ErrArtifactNotFound
	}
	copied := make([]byte, len(art.Data))
	copy(copied, art.Data)
	art.Data = copied
	return art, nil
}

// Delete removes an artifact and reclaims its byte budget.
func (s *RetainedArtifactStore) Delete(artifactID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	art, ok := s.artifacts[artifactID]
	if !ok {
		return false
	}
	delete(s.artifacts, artifactID)
	s.totalBytes -= int64(len(art.Data))
	return true
}

// ListByExecution returns summaries for all retained artifacts belonging to
// executionID, sorted deterministically by ArtifactID.
func (s *RetainedArtifactStore) ListByExecution(executionID string) []RetainedArtifactSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []RetainedArtifactSummary
	for _, art := range s.artifacts {
		if art.ExecutionID == executionID {
			out = append(out, art.RetainedArtifactSummary)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ArtifactID < out[j].ArtifactID
	})
	return out
}

// TotalBytes returns the aggregate retained byte count across the store.
func (s *RetainedArtifactStore) TotalBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.totalBytes
}
