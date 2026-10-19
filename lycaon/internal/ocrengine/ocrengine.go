// Package ocrengine resolves and verifies the bundled Linux text recognizer.
package ocrengine

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/configlayout"
)

// EnvRoot selects a staged engine directory instead of the installed payload.
// The payload is verified against the pin wherever it comes from.
const EnvRoot = "LYCAON_OCR_ROOT"

// EnvRequired fails a test that resolves no engine instead of skipping it.
const EnvRequired = "LYCAON_OCR_REQUIRED"

// DirName is the engine's directory under the engine resource root.
const DirName = "ocr"

// Unavailable reasons.
const (
	ReasonUnsupportedPlatform = "unsupported_platform"
	ReasonMissing             = "missing"
	ReasonDigestMismatch      = "digest_mismatch"
)

// UnavailableError reports why no verified engine resolved.
type UnavailableError struct {
	Reason string
	// Path is the payload that failed, for digest and missing reasons.
	Path string
}

func (e *UnavailableError) Error() string {
	switch e.Reason {
	case ReasonDigestMismatch:
		return fmt.Sprintf("ocrengine: %s differs from the pinned payload", e.Path)
	case ReasonUnsupportedPlatform:
		return "ocrengine: no bundled text recognizer for " + runtime.GOOS + "/" + runtime.GOARCH
	default:
		return "ocrengine: bundled text recognizer unavailable"
	}
}

// Engine is a verified, runnable text recognizer.
type Engine struct {
	// Root is the verified payload directory.
	Root string
	// Binary is the executable inside Root.
	Binary string
	// TessdataDir holds the trained data the executable loads.
	TessdataDir string
	// Version is the pinned engine version.
	Version string
	// LanguageArgument selects only the verified pinned recognition profile.
	LanguageArgument string
}

var (
	verifiedMu sync.Mutex
	verified   = map[string]Engine{}
)

// Resolve returns the verified bundled engine for this process. Every payload
// digest is checked once per root; a later call reuses that result.
func Resolve() (Engine, error) {
	pin, err := LoadPin()
	if err != nil {
		return Engine{}, err
	}
	return resolve(pin, runtime.GOOS, runtime.GOARCH, candidateRoots())
}

// resolve returns the first root that verifies for the platform. A missing
// root falls through to the next; a present but altered one stops resolution.
func resolve(pin *Pin, goos, goarch string, roots []string) (Engine, error) {
	plat, ok := pin.Platform(goos, goarch)
	if !ok {
		return Engine{}, &UnavailableError{Reason: ReasonUnsupportedPlatform}
	}
	var missing error
	for _, root := range roots {
		engine, err := verifyRoot(root, pin, plat)
		if err == nil {
			return engine, nil
		}
		var ue *UnavailableError
		if !errors.As(err, &ue) || ue.Reason != ReasonMissing {
			// A present but altered payload is never skipped over.
			return Engine{}, err
		}
		if missing == nil {
			missing = err
		}
	}
	if missing == nil {
		missing = &UnavailableError{Reason: ReasonMissing}
	}
	return Engine{}, missing
}

func candidateRoots() []string {
	var roots []string
	if root := strings.TrimSpace(os.Getenv(EnvRoot)); root != "" {
		roots = append(roots, root)
	}
	if root := configlayout.EngineRoot(); root != "" {
		roots = append(roots, filepath.Join(root, DirName))
	}
	if exe, err := os.Executable(); err == nil {
		if exe, err = filepath.EvalSymlinks(exe); err == nil {
			roots = append(roots, filepath.Join(filepath.Dir(exe), DirName))
		}
	}
	return roots
}

// verifyRoot checks every pinned payload under root. A verified root is
// remembered for the process; an absent or mismatched one is re-examined on
// the next call, so staging the engine later takes effect without a restart.
func verifyRoot(root string, pin *Pin, plat PlatformPin) (Engine, error) {
	if err := pin.validateLanguages(); err != nil {
		return Engine{}, err
	}
	root = filepath.Clean(root)
	verifiedMu.Lock()
	engine, ok := verified[root]
	verifiedMu.Unlock()
	if ok {
		return engine, nil
	}
	for _, payload := range pin.Payloads(plat) {
		path := filepath.Join(root, filepath.FromSlash(payload.Rel))
		if err := verifyPayload(path, payload); err != nil {
			return Engine{}, err
		}
	}
	binary := filepath.Join(root, filepath.FromSlash(binaryRel))
	if st, err := os.Stat(binary); err != nil || st.Mode()&0o111 == 0 {
		return Engine{}, &UnavailableError{Reason: ReasonMissing, Path: binary}
	}
	engine = Engine{
		Root:             root,
		Binary:           binary,
		TessdataDir:      filepath.Join(root, "tessdata"),
		Version:          pin.EngineVersion,
		LanguageArgument: strings.Join(pin.RecognitionLanguages, "+"),
	}
	verifiedMu.Lock()
	verified[root] = engine
	verifiedMu.Unlock()
	return engine, nil
}

func verifyPayload(path string, payload Payload) error {
	f, err := os.Open(path)
	if err != nil {
		return &UnavailableError{Reason: ReasonMissing, Path: path}
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return &UnavailableError{Reason: ReasonMissing, Path: path}
	}
	if st.Size() != payload.Bytes {
		return &UnavailableError{Reason: ReasonDigestMismatch, Path: path}
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, payload.Bytes+1)); err != nil {
		return &UnavailableError{Reason: ReasonMissing, Path: path}
	}
	if hex.EncodeToString(h.Sum(nil)) != payload.SHA256 {
		return &UnavailableError{Reason: ReasonDigestMismatch, Path: path}
	}
	return nil
}

// resetForTest forgets verified roots.
func resetForTest() {
	verifiedMu.Lock()
	verified = map[string]Engine{}
	verifiedMu.Unlock()
}
