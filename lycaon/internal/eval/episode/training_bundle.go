package episode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/trainingcapture"
)

// BundleRequest names one closed episode: the application capture, its
// training-capture directory, the delivered project and the root session.
type BundleRequest struct {
	Capture   string
	Captures  string
	Project   string
	SessionID string
	Out       string
}

// BundleManifest binds every bundle file and blob by SHA-256.
type BundleManifest struct {
	Schema        string            `json:"schema"`
	HostCommit    string            `json:"host_commit"`
	RootSessionID string            `json:"root_session_id"`
	ProjectID     string            `json:"project_id"`
	StoreSHA256   string            `json:"store_sha256"`
	Files         map[string]string `json:"files"`
	Blobs         []string          `json:"blobs"`
}

type bundleSession struct {
	ID          string `json:"id"`
	ParentID    string `json:"parent_id,omitempty"`
	ProjectID   string `json:"project_id"`
	MessagesRef string `json:"messages_blob"`
	Checkpoints any    `json:"checkpoints"`
	Workflows   any    `json:"workflows"`
	Verdicts    any    `json:"verdicts"`
}

type bundleObservation struct {
	Kind  string `json:"kind"`
	Value any    `json:"value"`
}

// bundleDecision is a host acceptance decision. A turn commit names the output
// whose settled message the host committed as the turn's visible result.
type bundleDecision struct {
	Kind      string `json:"kind"`
	SessionID string `json:"session_id"`
	MessageID string `json:"message_id"`
	OutputID  string `json:"output_id,omitempty"`
	DecidedAt string `json:"decided_at"`
}

type bundleCompletion struct {
	ID            string `json:"id"`
	SessionID     string `json:"session_id"`
	MessageID     string `json:"message_id"`
	TurnAttemptID string `json:"turn_attempt_id"`
	Iteration     int    `json:"iteration"`
	ProviderID    string `json:"provider_id"`
	Model         string `json:"model"`
	Scripted      bool   `json:"scripted"`
	FinishReason  string `json:"finish_reason"`
	SettledAt     string `json:"settled_at"`
	BodyRef       string `json:"body_blob"`
}

type sourceInventory struct {
	DeliveredPaths []string            `json:"delivered_paths"`
	Snapshots      map[string]Snapshot `json:"snapshots"`
	Promotions     map[string]string   `json:"promotions"`
}

// captureBodies are the capture fields large enough to live in blobs.
var captureBodies = []string{"request", "student_request", "response"}

type bundleWriter struct {
	out   string
	files map[string]string
	blobs map[string]bool
}

// WriteBundle exports one closed episode. It reads the capture without
// changing it and refuses an output directory that already exists.
func WriteBundle(ctx context.Context, req BundleRequest) (BundleManifest, error) {
	var manifest BundleManifest
	if req.Capture == "" || req.Captures == "" || req.SessionID == "" || req.Out == "" {
		return manifest, errors.New("bundle requires a capture, its training captures, a session and an output")
	}
	storeDigest, err := fileDigest(filepath.Join(req.Capture, "store.db"))
	if err != nil {
		return manifest, err
	}
	evidence, err := Read(ctx, req.Capture, req.SessionID)
	if err != nil {
		return manifest, err
	}
	if req.Project != "" {
		if err := evidence.ReadSources(ctx, req.Capture, req.Project); err != nil {
			return manifest, err
		}
	}
	database, err := openCapture(ctx, req.Capture)
	if err != nil {
		return manifest, err
	}
	defer func() { _ = database.Close() }()
	operations, err := evidence.ReadOperations(ctx, invocation.NewSQLRecorder(database))
	if err != nil {
		return manifest, err
	}
	outputs := map[string][]store.ModelOutput{}
	for _, session := range evidence.Sessions {
		outputs[session.ID], err = store.ReadModelOutputs(ctx, db.New(database), req.Capture, session.ID)
		if err != nil {
			return manifest, fmt.Errorf("session %s: %w", session.ID, err)
		}
	}
	if err := os.Mkdir(req.Out, 0o700); err != nil {
		return manifest, err
	}
	w := &bundleWriter{out: req.Out, files: map[string]string{}, blobs: map[string]bool{}}
	if err := os.Mkdir(filepath.Join(req.Out, "blobs"), 0o700); err != nil {
		return manifest, err
	}
	if err := w.write(ctx, evidence, operations, outputs, req.Captures); err != nil {
		return manifest, err
	}
	after, err := fileDigest(filepath.Join(req.Capture, "store.db"))
	if err != nil {
		return manifest, err
	}
	if after != storeDigest {
		return manifest, errors.New("application capture changed during export")
	}
	contract := Contract()
	manifest = BundleManifest{
		Schema: BundleSchema, HostCommit: contract.HostCommit, RootSessionID: evidence.RootSessionID,
		ProjectID: evidence.ProjectID, StoreSHA256: storeDigest, Files: w.files, Blobs: sortedKeys(w.blobs),
	}
	return manifest, w.json("manifest.json", manifest)
}

func (w *bundleWriter) write(ctx context.Context, evidence Evidence, operations []Operation, outputs map[string][]store.ModelOutput, captures string) error {
	if err := w.json("catalog.json", Contract()); err != nil {
		return err
	}
	inSession := map[string]bool{}
	var sessions []any
	for _, session := range evidence.Sessions {
		inSession[session.ID] = true
		ref, err := w.blob(session.Messages)
		if err != nil {
			return err
		}
		sessions = append(sessions, bundleSession{ID: session.ID, ParentID: session.ParentID, ProjectID: evidence.ProjectID,
			MessagesRef: ref, Checkpoints: session.Checkpoints, Workflows: session.Workflows, Verdicts: session.Verdicts})
	}
	captured, facts, err := w.captures(ctx, captures, inSession)
	if err != nil {
		return err
	}
	var decisions []any
	for _, fact := range facts {
		decisions = append(decisions, bundleDecision{Kind: fact.Kind, SessionID: fact.SessionID, MessageID: fact.MessageID,
			OutputID: committedOutput(outputs[fact.SessionID], fact), DecidedAt: fact.DecidedAt})
	}
	var completions []any
	for _, session := range evidence.Sessions {
		for _, output := range outputs[session.ID] {
			ref, err := w.blob(map[string]any{"content": output.Content, "tool_calls": json.RawMessage(nonEmpty(output.ToolCallsJSON))})
			if err != nil {
				return err
			}
			completions = append(completions, bundleCompletion{ID: output.ID, SessionID: output.SessionID, MessageID: output.MessageID,
				TurnAttemptID: output.TurnAttemptID, Iteration: output.Iteration, ProviderID: output.ProviderID, Model: output.Model,
				Scripted: output.Scripted, FinishReason: output.FinishReason, SettledAt: output.SettledAt.UTC().Format(time.RFC3339Nano), BodyRef: ref})
		}
	}
	var observations []any
	if evidence.Execution != nil {
		observations = append(observations, bundleObservation{Kind: "execution", Value: evidence.Execution})
	}
	if evidence.Allowance != nil {
		observations = append(observations, bundleObservation{Kind: "allowance", Value: evidence.Allowance})
	}
	for _, record := range evidence.Verification {
		observations = append(observations, bundleObservation{Kind: "verification", Value: record})
	}
	for _, worker := range evidence.Workers {
		observations = append(observations, bundleObservation{Kind: "worker", Value: worker})
	}
	var effects []any
	for _, effect := range evidence.Effects {
		effects = append(effects, effect)
	}
	var opLines []any
	for _, op := range operations {
		opLines = append(opLines, op)
	}
	for name, lines := range map[string][]any{
		"sessions.jsonl": sessions, "captures.jsonl": captured, "operations.jsonl": opLines, "observations.jsonl": observations,
		"effects.jsonl": effects, "decisions.jsonl": decisions, "completions.jsonl": completions,
	} {
		if err := w.lines(name, lines); err != nil {
			return err
		}
	}
	return w.json("source-inventory.json", sourceInventory{DeliveredPaths: nonNil(evidence.DeliveredPaths),
		Snapshots: evidence.Snapshots, Promotions: evidence.Promotions})
}

// captures returns this tree's capture records, bodies moved to blobs, and its
// facts, both in file-name order.
func (w *bundleWriter) captures(ctx context.Context, dir string, inSession map[string]bool) ([]any, []trainingcapture.Fact, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var records []any
	var facts []trainingcapture.Fact
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, nil, err
		}
		var record map[string]json.RawMessage
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil, nil, fmt.Errorf("capture %s: %w", entry.Name(), err)
		}
		var schema, session string
		_ = json.Unmarshal(record["schema"], &schema)
		_ = json.Unmarshal(record["session_id"], &session)
		if !inSession[session] {
			continue
		}
		switch schema {
		case trainingcapture.FactSchema:
			var fact trainingcapture.Fact
			if err := json.Unmarshal(raw, &fact); err != nil {
				return nil, nil, fmt.Errorf("capture %s: %w", entry.Name(), err)
			}
			facts = append(facts, fact)
		case trainingcapture.Schema:
			for _, field := range captureBodies {
				body, ok := record[field]
				if !ok || len(body) == 0 || string(body) == "null" {
					continue
				}
				ref, err := w.rawBlob(body)
				if err != nil {
					return nil, nil, err
				}
				delete(record, field)
				blob, err := json.Marshal(ref)
				if err != nil {
					return nil, nil, fmt.Errorf("capture %s: %w", entry.Name(), err)
				}
				record[field+"_blob"] = blob
			}
			records = append(records, record)
		default:
			return nil, nil, fmt.Errorf("capture %s has unknown schema %q", entry.Name(), schema)
		}
	}
	return records, facts, nil
}

// committedOutput is the output whose settled message the host committed: the
// latest one for that message settled no later than the commit.
func committedOutput(outputs []store.ModelOutput, fact trainingcapture.Fact) string {
	decided, err := time.Parse(time.RFC3339Nano, fact.DecidedAt)
	if err != nil {
		return ""
	}
	var chosen *store.ModelOutput
	for i := range outputs {
		output := &outputs[i]
		if output.MessageID != fact.MessageID || output.SettledAt.IsZero() || output.SettledAt.After(decided) {
			continue
		}
		if chosen == nil || output.SettledAt.After(chosen.SettledAt) {
			chosen = output
		}
	}
	if chosen == nil {
		return ""
	}
	return chosen.ID
}

func (w *bundleWriter) blob(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return w.rawBlob(raw)
}

func (w *bundleWriter) rawBlob(raw []byte) (string, error) {
	sum := sha256.Sum256(raw)
	name := hex.EncodeToString(sum[:])
	if !w.blobs[name] {
		if err := os.WriteFile(filepath.Join(w.out, "blobs", name), raw, 0o600); err != nil {
			return "", err
		}
		w.blobs[name] = true
	}
	return name, nil
}

func (w *bundleWriter) lines(name string, values []any) error {
	var buf bytes.Buffer
	for _, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		buf.Write(raw)
		buf.WriteByte('\n')
	}
	return w.file(name, buf.Bytes())
}

func (w *bundleWriter) json(name string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return w.file(name, append(raw, '\n'))
}

func (w *bundleWriter) file(name string, raw []byte) error {
	if err := os.WriteFile(filepath.Join(w.out, name), raw, 0o600); err != nil {
		return err
	}
	if name != "manifest.json" {
		sum := sha256.Sum256(raw)
		w.files[name] = hex.EncodeToString(sum[:])
	}
	return nil
}

func fileDigest(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func nonEmpty(raw string) string {
	if raw == "" {
		return "[]"
	}
	return raw
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return slices.Clone(values)
}
