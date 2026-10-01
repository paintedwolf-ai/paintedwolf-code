// Package editoroutbox defines the durable native editor envelope shared with
// Den. The host inventories and validates it without interpreting CRDT history.
package editoroutbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/filelock"
)

//go:embed format.json
var formatJSON []byte

type layout struct {
	Version            int    `json:"version"`
	Directory          string `json:"directory"`
	Header             string `json:"header"`
	LogPrefix          string `json:"log_prefix"`
	LogSuffix          string `json:"log_suffix"`
	Lock               string `json:"lock"`
	MaxDocumentBytes   int64  `json:"max_document_bytes"`
	MaxRecordBytes     int64  `json:"max_record_bytes"`
	MaxHeaderBytes     int64  `json:"max_header_bytes"`
	CompactionBytes    int64  `json:"compaction_bytes"`
	CacheBytes         int64  `json:"cache_bytes"`
	CacheDocuments     int    `json:"cache_documents"`
	CacheMaxAgeSeconds int64  `json:"cache_max_age_seconds"`
}

var format = func() layout {
	var value layout
	if err := json.Unmarshal(formatJSON, &value); err != nil {
		panic(err)
	}
	return value
}()

// Directory is durable user work, not a clearable cache.
func Directory() string { return format.Directory }

// Acquire excludes native commits while the host captures or replaces the store.
// Take this before the SQLite snapshot so acknowledgement removal cannot race it.
func Acquire(ctx context.Context, root string) (func(), error) {
	file, err := filelock.Open(filepath.Join(root, format.Lock))
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if contextErr := ctx.Err(); contextErr != nil {
			err = contextErr
			break
		}
		var acquired bool
		acquired, err = filelock.TryExclusive(file)
		if err != nil {
			break
		}
		if acquired {
			return func() { _ = file.Close() }, nil
		}
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-ticker.C:
			continue
		}
		break
	}
	_ = file.Close()
	return nil, err
}

type address struct {
	DocumentID string `json:"documentId"`
	ProjectID  string `json:"projectId"`
	RootID     string `json:"rootId"`
	FileID     string `json:"fileId"`
	Path       string `json:"path"`
}

// frame locates one preserved record; checkpoint bookkeeping lives beside it.
type frame struct {
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Offset       int64    `json:"offset"`
	Length       int64    `json:"length"`
	SHA256       string   `json:"sha256"`
	Pending      []string `json:"pending,omitempty"`
	Synchronized *bool    `json:"synchronized,omitempty"`
}

type header struct {
	Format          int      `json:"format"`
	Address         address  `json:"address"`
	UpdatedAt       uint64   `json:"updatedAt"`
	Synchronized    bool     `json:"synchronized"`
	Log             string   `json:"log"`
	LogBytes        int64    `json:"logBytes"`
	Frames          []frame  `json:"frames"`
	RetainedClients []string `json:"retainedClients,omitempty"`
}

// Validate refuses unknown envelopes before restore can replace live work.
func Validate(ctx context.Context, root string) error {
	directory := filepath.Join(root, format.Directory)
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("editor outbox root is not a directory")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() {
			return fmt.Errorf("editor outbox: invalid directory %s", entry.Name())
		}
		if err = validateDocument(filepath.Join(directory, entry.Name()), entry.Name()); err != nil {
			return fmt.Errorf("editor outbox: %s: %w", entry.Name(), err)
		}
	}
	return nil
}

// logGeneration accepts records-<n>.log names and their temporary files.
func logGeneration(name string) (int, bool) {
	name = strings.TrimSuffix(name, ".tmp")
	if !strings.HasPrefix(name, format.LogPrefix) || !strings.HasSuffix(name, format.LogSuffix) {
		return 0, false
	}
	generation, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, format.LogPrefix), format.LogSuffix))
	return generation, err == nil && generation > 0
}

func validateDocument(directory, key string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		_, isLog := logGeneration(name)
		if (name != format.Header && name != format.Header+".tmp" && !isLog) || !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported envelope entry")
		}
	}
	path := filepath.Join(directory, format.Header)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil // Only an interrupted, uncommitted transaction may remain.
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > format.MaxHeaderBytes {
		return fmt.Errorf("invalid envelope header")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var h header
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&h); err != nil {
		return err
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("invalid envelope header trailing data")
	}
	identity := sha256.Sum256([]byte(h.Address.DocumentID))
	if _, isLog := logGeneration(h.Log); h.Format != format.Version || h.LogBytes < 0 || h.LogBytes > format.MaxDocumentBytes || !isLog || strings.HasSuffix(h.Log, ".tmp") || hex.EncodeToString(identity[:]) != key {
		return fmt.Errorf("unsupported or inconsistent envelope")
	}
	for _, value := range []string{h.Address.DocumentID, h.Address.ProjectID, h.Address.RootID, h.Address.FileID} {
		if value == "" || len(value) > 256 {
			return fmt.Errorf("invalid document address")
		}
	}
	if h.Address.Path == "" || len(h.Address.Path) > 4096 {
		return fmt.Errorf("invalid document path")
	}
	if len(h.Frames) == 0 {
		return fmt.Errorf("empty envelope")
	}
	log, err := os.Open(filepath.Join(directory, h.Log))
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	logInfo, err := log.Stat()
	if err != nil {
		return err
	}
	if !logInfo.Mode().IsRegular() || logInfo.Size() < h.LogBytes {
		return fmt.Errorf("envelope records are shorter than their header")
	}
	return validateFrames(h, log)
}

func validateFrames(h header, log io.ReaderAt) error {
	keys := make(map[string]bool, len(h.Frames))
	synchronized := true
	for _, f := range h.Frames {
		if f.Length <= 0 || f.Length > format.MaxRecordBytes || f.Offset < 0 || f.Offset > h.LogBytes-f.Length {
			return fmt.Errorf("frame outside the committed log")
		}
		raw := make([]byte, f.Length)
		if _, err := log.ReadAt(raw, f.Offset); err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != f.SHA256 || raw[len(raw)-1] != '\n' {
			return fmt.Errorf("envelope checksum mismatch")
		}
		var record struct {
			Kind        string `json:"kind"`
			DocumentID  string `json:"documentId"`
			ClientID    string `json:"clientId"`
			OperationID string `json:"operationId"`
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			return err
		}
		switch record.Kind {
		case "checkpoint":
			record.OperationID = "checkpoint"
		case "update", "command", "format":
		default:
			return fmt.Errorf("unknown record kind")
		}
		if record.Kind != f.Kind || record.DocumentID != h.Address.DocumentID || record.ClientID == "" || len(record.ClientID) > 256 || record.OperationID == "" || len(record.OperationID) > 256 {
			return fmt.Errorf("invalid record identity")
		}
		name := sha256.Sum256([]byte(record.ClientID + "\x00" + record.Kind + "\x00" + record.OperationID))
		if hex.EncodeToString(name[:]) != f.Name {
			return fmt.Errorf("record identity does not match its frame")
		}
		if keys[f.Name] {
			return fmt.Errorf("duplicate record")
		}
		keys[f.Name] = true
		synchronized = synchronized && record.Kind == "checkpoint" && f.Synchronized != nil && *f.Synchronized
	}
	clients := make(map[string]bool, len(h.RetainedClients))
	for _, client := range h.RetainedClients {
		name := sha256.Sum256([]byte(client + "\x00checkpoint\x00checkpoint"))
		if client == "" || len(client) > 256 || clients[client] || !keys[hex.EncodeToString(name[:])] {
			return fmt.Errorf("invalid checkpoint retention reference")
		}
		clients[client] = true
	}
	if synchronized != h.Synchronized {
		return fmt.Errorf("acknowledgement summary mismatch")
	}
	return nil
}
