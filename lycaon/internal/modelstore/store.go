// Package modelstore owns the durable lifecycle of local model artifacts:
// staging, verification, activation and repair.
//
// The lifecycle is absent → staging → verified → active. Only verified
// artifacts activate; activation is an append-only journal entry so a crash
// between file placement and activation is recoverable.
package modelstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
)

// Artifact identifies one immutable model payload.
type Artifact struct {
	// Digest is the sha256 of the payload, hex-encoded.
	Digest string `json:"digest"`
	// Format names the layout (safetensors, gguf).
	Format string `json:"format"`
	// Bytes is the payload size.
	Bytes int64 `json:"bytes"`
}

// Manifest describes one model revision: the payload and how to serve it.
type Manifest struct {
	// Model is the stable model identity.
	Model string `json:"model"`
	// Revision distinguishes builds of the same model.
	Revision string `json:"revision"`
	// Artifact is the generation payload.
	Artifact Artifact `json:"artifact"`
	// Device names the backend recipe (cpu, mlx, vulkan).
	Device string `json:"device"`
}

// Store roots the on-disk layout: artifacts by digest, journal, manifests.
type Store struct {
	root string
}

func New(root string) *Store { return &Store{root: root} }

func (s *Store) artifactPath(digest string) string {
	return filepath.Join(s.root, "artifacts", digest[:2], digest)
}

func (s *Store) journalPath() string { return filepath.Join(s.root, "journal.jsonl") }

// Stage copies a payload into the store under its digest, if absent.
// It verifies the hash while copying; a mismatch leaves nothing behind.
func (s *Store) Stage(r io.Reader, want Artifact) error {
	if err := os.MkdirAll(filepath.Dir(s.artifactPath(want.Digest)), 0o755); err != nil {
		return fmt.Errorf("modelstore: mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.artifactPath(want.Digest)), ".staging-*")
	if err != nil {
		return fmt.Errorf("modelstore: temp: %w", err)
	}
	defer os.Remove(tmp.Name())

	var h hash.Hash = sha256.New()
	var n int64
	if _, err := io.Copy(io.MultiWriter(tmp, h), func() io.Reader { return &counting{r, &n} }()); err != nil {
		tmp.Close()
		return fmt.Errorf("modelstore: copy: %w", err)
	}
	tmp.Close()
	if got := hex.EncodeToString(h.Sum(nil)); got != want.Digest {
		return fmt.Errorf("modelstore: digest mismatch: got %s want %s", got, want.Digest)
	}
	if n != want.Bytes {
		return fmt.Errorf("modelstore: size mismatch: got %d want %d", n, want.Bytes)
	}
	if err := os.Rename(tmp.Name(), s.artifactPath(want.Digest)); err != nil {
		return fmt.Errorf("modelstore: publish: %w", err)
	}
	return nil
}

type counting struct {
	r io.Reader
	n *int64
}

func (c *counting) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	*c.n += int64(n)
	return n, err
}

// Verified reports whether an artifact is fully present with the right hash.
func (s *Store) Verified(digest string) bool {
	f, err := os.Open(s.artifactPath(digest))
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == digest
}

// Activate records a manifest in the journal, making it the store's intent.
// The entry is idempotent per (model, revision, device).
func (s *Store) Activate(m Manifest) error {
	if !s.Verified(m.Artifact.Digest) {
		return fmt.Errorf("modelstore: artifact %s is not verified", m.Artifact.Digest)
	}
	f, err := os.OpenFile(s.journalPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("modelstore: journal: %w", err)
	}
	defer f.Close()
	line, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("modelstore: append: %w", err)
	}
	return nil
}

// Active returns the newest journaled manifest per (model, device).
func (s *Store) Active() (map[string]Manifest, error) {
	raw, err := os.ReadFile(s.journalPath())
	if os.IsNotExist(err) {
		return map[string]Manifest{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("modelstore: read journal: %w", err)
	}
	out := map[string]Manifest{}
	for _, line := range splitLines(raw) {
		var m Manifest
		if err := json.Unmarshal(line, &m); err != nil {
			continue // a torn final line is recoverable; later entries win
		}
		out[m.Model+"/"+m.Device] = m
	}
	return out, nil
}

func splitLines(raw []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range raw {
		if b == '\n' {
			if i > start {
				out = append(out, raw[start:i])
			}
			start = i + 1
		}
	}
	if start < len(raw) {
		out = append(out, raw[start:])
	}
	return out
}
