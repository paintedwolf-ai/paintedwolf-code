package briefing

import (
	"context"
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

// Results stores section text by manifest hash. Put inserts, or upgrades a
// fallback to a summarizer result; it never replaces summarizer text.
type Results interface {
	GetResults(ctx context.Context, projectID string, hashes []string) (map[string]Result, error)
	PutResult(ctx context.Context, projectID string, manifest Manifest, hash string, result Result) error
}

// Attempts keeps target attempt state. Complete applies only for the active
// attempt, so a superseded attempt cannot publish over its successor.
type Attempts interface {
	StartAttempt(ctx context.Context, request Request, hashes []SectionHash) (attemptID string, joined bool, err error)
	CompleteAttempt(ctx context.Context, request Request, attemptID string, sections []SectionHash, recordID string) (applied bool, err error)
	FailAttempt(ctx context.Context, request Request, attemptID string, message string) (applied bool, err error)
}

// Records keeps the immutable record a completed target points at.
type Records interface {
	PutRecord(ctx context.Context, projectID, recordID, profileID, version string, sections []SectionHash) error
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
