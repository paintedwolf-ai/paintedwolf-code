package briefingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/contentblob"
	"github.com/lycaon/lycaon/internal/db"
)

const (
	// maxManifestBytes bounds a manifest's displayable form.
	maxManifestBytes = 256 << 10
	// lookupChunk bounds the keys one read binds.
	lookupChunk = 64
)

// Store persists briefing state: content-addressed results and records,
// per-target attempt state, and what retention keeps.
type Store struct {
	*Results
	*Targets
	*Retention
}

var _ briefing.Store = (*Store)(nil)

func NewStore(database db.Handle, dataDir string) *Store {
	core := storeCore{database: database, queries: db.New(database), dataDir: dataDir,
		now: func() time.Time { return time.Now().UTC() }, newID: uuid.NewString}
	return &Store{Results: &Results{core}, Targets: &Targets{core}, Retention: &Retention{core}}
}

// Results holds section results by manifest hash and the records built
// from them.
type Results struct{ storeCore }

type storeCore struct {
	database db.Handle
	queries  *db.Queries
	dataDir  string
	now      func() time.Time
	newID    func() string
}

func (s storeCore) inTx(ctx context.Context, fn func(q *db.Queries) error) error {
	return db.InTx(ctx, s.database, s.queries, nil, func(q *db.Queries, _ *sql.Tx) error { return fn(q) })
}

func (s storeCore) stamp() string { return db.FormatTime(s.now()) }

// Epoch returns the project's cache epoch, starting it at 1.
func (s *Results) Epoch(ctx context.Context, projectID string) (int64, error) {
	var epoch int64
	err := s.inTx(ctx, func(q *db.Queries) error {
		if err := q.EnsureBriefingEpoch(ctx, projectID); err != nil {
			return err
		}
		var err error
		epoch, err = q.GetBriefingEpoch(ctx, projectID)
		return err
	})
	return epoch, err
}

func checkEpoch(ctx context.Context, q *db.Queries, projectID string, epoch int64) error {
	current, err := q.GetBriefingEpoch(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && current != epoch) {
		return briefing.ErrStaleEpoch
	}
	return err
}

func (s *Results) GetResults(ctx context.Context, projectID string, hashes []string) (map[string]briefing.Result, error) {
	out := make(map[string]briefing.Result, len(hashes))
	for start := 0; start < len(hashes); start += lookupChunk {
		rows, err := s.queries.ListBriefingResults(ctx, db.ListBriefingResultsParams{ProjectID: projectID, Hashes: hashes[start:min(start+lookupChunk, len(hashes))]})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			result := briefing.Result{Text: row.Text, Source: briefing.Source(row.Source), Provider: row.ProviderID, Model: row.Model}
			if err := json.Unmarshal([]byte(row.QualityJson), &result.Quality); err != nil {
				return nil, fmt.Errorf("decode briefing quality %s: %w", row.ManifestHash, err)
			}
			if result.CreatedAt, err = db.ParseTime(row.CreatedAt); err != nil {
				return nil, err
			}
			out[row.ManifestHash] = result
		}
	}
	return out, nil
}

// PutResult persists the manifest once, then inserts the result or upgrades a
// fallback. Text over the section's bound is refused at this boundary.
func (s *Results) PutResult(ctx context.Context, projectID string, epoch int64, manifest briefing.Manifest, hash string, result briefing.Result) error {
	if bound := manifest.Contract.MaxRunes; bound > 0 && utf8.RuneCountInString(result.Text) > bound {
		return fmt.Errorf("briefing result for %s exceeds %d runes", manifest.SectionID, bound)
	}
	blob, err := s.manifestBlob(ctx, projectID, manifest, hash)
	if err != nil {
		return err
	}
	quality, err := json.Marshal(append([]string{}, result.Quality...))
	if err != nil {
		return err
	}
	coverage, err := json.Marshal(manifest.Coverage())
	if err != nil {
		return err
	}
	created := result.CreatedAt
	if created.IsZero() {
		created = s.now()
	}
	return s.inTx(ctx, func(q *db.Queries) error {
		if err := checkEpoch(ctx, q, projectID, epoch); err != nil {
			return err
		}
		if blob.size > 0 {
			if err := q.UpsertContentBlobObject(ctx, db.UpsertContentBlobObjectParams{ProjectID: projectID, Sha256: blob.sha, ByteSize: blob.size, StoredSize: blob.stored, CreatedAt: s.stamp()}); err != nil {
				return err
			}
		}
		if err := q.InsertBriefingManifest(ctx, db.InsertBriefingManifestParams{
			ProjectID: projectID, ManifestHash: hash, ProfileID: manifest.ProfileID, ProfileVersion: manifest.ProfileVersion,
			SectionID: manifest.SectionID, TemplateID: manifest.Template.ID, TemplateVersion: manifest.Template.Version,
			ContentBlobSha256: blob.sha, CoverageJson: string(coverage), UnavailableInputs: int64(manifest.Unavailable()), CreatedAt: s.stamp(),
		}); err != nil {
			return err
		}
		_, err := q.PutBriefingResult(ctx, db.PutBriefingResultParams{
			ProjectID: projectID, ManifestHash: hash, Text: result.Text, Source: string(result.Source),
			ProviderID: result.Provider, Model: result.Model, QualityJson: string(quality), CreatedAt: db.FormatTime(created),
		})
		return err
	})
}

type manifestBlobRef struct {
	sha          string
	size, stored int64
}

// manifestBlob writes the displayable manifest unless it is already stored.
// A stored manifest reports no size, so its blob row is left alone.
func (s *Results) manifestBlob(ctx context.Context, projectID string, manifest briefing.Manifest, hash string) (manifestBlobRef, error) {
	existing, err := s.queries.GetBriefingManifestBlob(ctx, db.GetBriefingManifestBlobParams{ProjectID: projectID, ManifestHash: hash})
	if err == nil {
		return manifestBlobRef{sha: existing}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return manifestBlobRef{}, err
	}
	display, err := manifest.Display()
	if err != nil {
		return manifestBlobRef{}, err
	}
	if len(display) > maxManifestBytes {
		return manifestBlobRef{}, fmt.Errorf("briefing manifest %s is %d bytes; the bound is %d", hash, len(display), maxManifestBytes)
	}
	sha, size, stored, err := contentblob.Write(ctx, contentblob.StoreFor(s.dataDir, projectID), display)
	if err != nil {
		return manifestBlobRef{}, fmt.Errorf("write briefing manifest: %w", err)
	}
	return manifestBlobRef{sha: sha, size: size, stored: stored}, nil
}

// ReadManifest returns a stored manifest's displayable form.
func (s *Results) ReadManifest(ctx context.Context, projectID, hash string) ([]byte, error) {
	sha, err := s.queries.GetBriefingManifestBlob(ctx, db.GetBriefingManifestBlobParams{ProjectID: projectID, ManifestHash: hash})
	if err != nil {
		return nil, err
	}
	return contentblob.Read(contentblob.StoreFor(s.dataDir, projectID), sha)
}

// PutRecord stores a record and its ordered sections once; its method is
// derived from the sections' stored sources.
func (s *Results) PutRecord(ctx context.Context, projectID string, epoch int64, record briefing.Record) error {
	origin, err := json.Marshal(record.Origin)
	if err != nil {
		return err
	}
	hashes := make([]string, len(record.Sections))
	for i, section := range record.Sections {
		hashes[i] = section.ManifestHash
	}
	return s.inTx(ctx, func(q *db.Queries) error {
		if err := checkEpoch(ctx, q, projectID, epoch); err != nil {
			return err
		}
		method, err := recordMethod(ctx, q, projectID, hashes)
		if err != nil {
			return err
		}
		inserted, err := q.InsertBriefingRecord(ctx, db.InsertBriefingRecordParams{
			ProjectID: projectID, RecordID: record.ID, ProfileID: record.ProfileID, ProfileVersion: record.Version,
			Method: method, OriginJson: string(origin), CreatedAt: s.stamp(),
		})
		if err != nil || inserted == 0 {
			return err
		}
		for i, section := range record.Sections {
			if err := q.InsertBriefingRecordSection(ctx, db.InsertBriefingRecordSectionParams{
				ProjectID: projectID, RecordID: record.ID, Ordinal: int64(i), SectionID: section.SectionID, ManifestHash: section.ManifestHash,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

func recordMethod(ctx context.Context, q *db.Queries, projectID string, hashes []string) (string, error) {
	model, other := false, false
	for start := 0; start < len(hashes); start += lookupChunk {
		rows, err := q.ListBriefingResults(ctx, db.ListBriefingResultsParams{ProjectID: projectID, Hashes: hashes[start:min(start+lookupChunk, len(hashes))]})
		if err != nil {
			return "", err
		}
		if len(rows) != min(lookupChunk, len(hashes)-start) {
			return "", fmt.Errorf("briefing record names a section without a stored result")
		}
		for _, row := range rows {
			if briefing.Source(row.Source) == briefing.SourceSummarizer {
				model = true
			} else {
				other = true
			}
		}
	}
	switch {
	case model && other:
		return "mixed", nil
	case model:
		return "model", nil
	default:
		return "deterministic", nil
	}
}

// RecordView is a stored record with its ordered sections.
type RecordView struct {
	ID        string
	ProfileID string
	Version   string
	Method    string
	Origin    briefing.RecordOrigin
	Sections  []briefing.SectionHash
	CreatedAt time.Time
}

func (s *Results) GetRecord(ctx context.Context, projectID, recordID string) (RecordView, error) {
	row, err := s.queries.GetBriefingRecord(ctx, db.GetBriefingRecordParams{ProjectID: projectID, RecordID: recordID})
	if err != nil {
		return RecordView{}, err
	}
	view := RecordView{ID: row.RecordID, ProfileID: row.ProfileID, Version: row.ProfileVersion, Method: row.Method}
	if err := json.Unmarshal([]byte(row.OriginJson), &view.Origin); err != nil {
		return RecordView{}, err
	}
	if view.CreatedAt, err = db.ParseTime(row.CreatedAt); err != nil {
		return RecordView{}, err
	}
	sections, err := s.queries.ListBriefingRecordSections(ctx, db.ListBriefingRecordSectionsParams{ProjectID: projectID, RecordID: recordID})
	if err != nil {
		return RecordView{}, err
	}
	for _, section := range sections {
		view.Sections = append(view.Sections, briefing.SectionHash{SectionID: section.SectionID, ManifestHash: section.ManifestHash})
	}
	return view, nil
}
