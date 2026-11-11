package briefing

import (
	"context"
	"errors"
	"time"
)

// Source records how a section's text was produced.
type Source string

const (
	SourceSummarizer    Source = "summarizer"
	SourceFallback      Source = "fallback"
	SourceDeterministic Source = "deterministic"
)

// Result is a section's stored text for one manifest hash.
type Result struct {
	Text      string
	Source    Source
	Provider  string
	Model     string
	Quality   []string
	CreatedAt time.Time
}

// ErrStaleEpoch reports a write admitted before the project's briefings were
// purged.
var ErrStaleEpoch = errors.New("briefing epoch changed")

// Results stores section text by manifest hash. Put inserts, or upgrades a
// fallback to a summarizer result; it never replaces summarizer text.
type Results interface {
	Epoch(ctx context.Context, projectID string) (int64, error)
	GetResults(ctx context.Context, projectID string, hashes []string) (map[string]Result, error)
	PutResult(ctx context.Context, projectID string, epoch int64, manifest Manifest, hash string, result Result) error
}

// Attempt is the target attempt a request started or joined.
type Attempt struct {
	ID       string
	Joined   bool
	Epoch    int64
	Revision int64
}

// Settled is a target mutation's outcome. Applied is false when the attempt
// was no longer the target's active one.
type Settled struct {
	Applied  bool
	Revision int64
}

// Failure is why an attempt ended without a record.
type Failure struct {
	Code    string
	Message string
}

const (
	CodeGenerationFailed = "generation_failed"
	CodeQueueExpired     = "queue_expired"
	CodeAdmissionRefused = "admission_refused"
	CodeInterrupted      = "interrupted"
	// CodeCancelled ends an attempt the scheduler stopped; its status is
	// cancelled rather than failed.
	CodeCancelled = "cancelled"
)

// Attempts keeps target state. Complete and Fail apply only for the active
// attempt in the current epoch, so a superseded attempt cannot publish over
// its successor or resurrect a purged target.
type Attempts interface {
	StartAttempt(ctx context.Context, request Request, epoch int64, sections []SectionHash) (Attempt, error)
	CompleteAttempt(ctx context.Context, request Request, attemptID string, sections []SectionHash, recordID string) (Settled, error)
	FailAttempt(ctx context.Context, request Request, attemptID string, failure Failure) (Settled, error)
	// Resolve points the target at a record that needed no attempt.
	Resolve(ctx context.Context, request Request, epoch int64, sections []SectionHash, recordID string) (Settled, error)
}

// Record is an immutable set of section results.
type Record struct {
	ID        string
	ProfileID string
	Version   string
	Sections  []SectionHash
	Origin    RecordOrigin
}

// RecordOrigin is the attempt that first created a record.
type RecordOrigin struct {
	AttemptID string `json:"attempt_id,omitempty"`
	Trigger   string `json:"trigger,omitempty"`
}

// Records keeps the immutable record a completed target points at.
type Records interface {
	PutRecord(ctx context.Context, projectID string, epoch int64, record Record) error
}

// Store is the persistence the engine needs.
type Store interface {
	Results
	Attempts
	Records
}

// Generator invokes the utility model for one section.
type Generator interface {
	Generate(ctx context.Context, invocation Invocation) (text string, provider, model string, err error)
}

// Invocation is one utility call. Requested binds the class for work a person
// is waiting on; other work binds the overlay class.
type Invocation struct {
	ProjectID  string
	ProjectDir string
	Purpose    string
	Requested  bool
	Prompt     Prompt
}

// Publisher announces committed snapshots.
type Publisher interface {
	PublishSnapshot(ctx context.Context, projectID string, snapshot Snapshot)
}
