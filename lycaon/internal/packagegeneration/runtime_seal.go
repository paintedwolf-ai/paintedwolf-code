package packagegeneration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
)

// Runtime seals describe administrator-derived bytes, separately from publisher
// manifest v2. Unknown shapes refuse without changing either retained artifact.
const RuntimeSealVersion = 1

// These provisional format ceilings bound decoding, not an operational promise
// that every selected runtime fits. Larger formats require explicit review.
const RuntimeSealMaxBytes = 16 << 20
const RuntimeSealMaxEntries = 32768

// RuntimeSeal records content relationships; it grants no filesystem authority.
type RuntimeSeal struct {
	FormatVersion      int             `json:"format_version"`
	BaseManifestSHA256 string          `json:"base_manifest_sha256"`
	Architecture       string          `json:"architecture"`
	InventoryPath      string          `json:"inventory_path"`
	InventorySHA256    string          `json:"inventory_sha256"`
	ProducerVersion    string          `json:"producer_version"`
	Sources            []RuntimeSource `json:"sources"`
	Files              []RuntimeFile   `json:"files"`
	Aliases            []RuntimeAlias  `json:"aliases"`
	Anchors            []RuntimeAnchor `json:"anchors"`
}

type RuntimeSource struct {
	Path         string             `json:"path"`
	CapturedPath string             `json:"captured_path"`
	SHA256       string             `json:"sha256"`
	Size         int64              `json:"size"`
	Origin       RuntimeOrigin      `json:"origin"`
	Observation  *KernelObservation `json:"observation"`
}

type RuntimeOrigin struct {
	Kind         string `json:"kind"`
	SelectedRoot string `json:"selected_root"`
	StoreRoot    string `json:"store_root"`
	OutputPath   string `json:"output_path"`
}

// KernelObservation is diagnostic provenance, never portable Nix authority.
type KernelObservation struct {
	Device  uint64 `json:"device"`
	Inode   uint64 `json:"inode"`
	MtimeNS int64  `json:"mtime_ns"`
	CtimeNS int64  `json:"ctime_ns"`
	UID     uint32 `json:"uid"`
	GID     uint32 `json:"gid"`
	Mode    uint32 `json:"mode"`
}

type RuntimeFile struct {
	Path   string  `json:"path"`
	Kind   string  `json:"kind"`
	Mode   uint32  `json:"mode"`
	SHA256 *string `json:"sha256"`
	Size   *int64  `json:"size"`
	Target *string `json:"target"`
}

type RuntimeAlias struct {
	Path   string `json:"path"`
	Target string `json:"target"`
	Kind   string `json:"kind"`
}

// Role is a label, not authorization. Production consumers require their exact
// closed role set and independently authenticate each selected ELF image.
type RuntimeAnchor struct {
	Role   string `json:"role"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// MarshalRuntimeSeal sorts copies, validates relationships, and hashes the exact
// canonical JSON plus newline. It never changes the caller's order or bytes.
func MarshalRuntimeSeal(seal *RuntimeSeal) ([]byte, string, error) {
	if seal == nil {
		return nil, "", errors.New("runtime seal: missing document")
	}
	canonical := *seal
	canonical.Sources = slices.Clone(seal.Sources)
	canonical.Files = slices.Clone(seal.Files)
	canonical.Aliases = slices.Clone(seal.Aliases)
	canonical.Anchors = slices.Clone(seal.Anchors)
	slices.SortFunc(canonical.Sources, func(a, b RuntimeSource) int { return strings.Compare(a.Path, b.Path) })
	slices.SortFunc(canonical.Files, func(a, b RuntimeFile) int { return strings.Compare(a.Path, b.Path) })
	slices.SortFunc(canonical.Aliases, func(a, b RuntimeAlias) int { return strings.Compare(a.Path, b.Path) })
	slices.SortFunc(canonical.Anchors, func(a, b RuntimeAnchor) int { return strings.Compare(a.Role, b.Role) })
	if err := canonical.Validate(); err != nil {
		return nil, "", err
	}
	raw, err := json.Marshal(&canonical)
	if err != nil {
		return nil, "", fmt.Errorf("runtime seal: encode: %w", err)
	}
	raw = append(raw, '\n')
	if len(raw) > RuntimeSealMaxBytes {
		return nil, "", errors.New("runtime seal: document exceeds format ceiling")
	}
	return raw, runtimeDigest(raw), nil
}

func ReadRuntimeSeal(reader io.Reader) (*RuntimeSeal, []byte, string, error) {
	if reader == nil {
		return nil, nil, "", errors.New("runtime seal: missing reader")
	}
	raw, err := io.ReadAll(io.LimitReader(reader, RuntimeSealMaxBytes+1))
	if err != nil {
		return nil, nil, "", fmt.Errorf("runtime seal: read: %w", err)
	}
	if len(raw) > RuntimeSealMaxBytes {
		return nil, nil, "", errors.New("runtime seal: document exceeds format ceiling")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var seal RuntimeSeal
	if err := decoder.Decode(&seal); err != nil {
		return nil, nil, "", fmt.Errorf("runtime seal: decode: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, nil, "", errors.New("runtime seal: trailing document")
	}
	canonical, identity, err := MarshalRuntimeSeal(&seal)
	if err != nil {
		return nil, nil, "", err
	}
	// This also rejects duplicate/missing JSON fields, alternate escaping,
	// unsorted arrays and noncanonical numbers without accepting two identities.
	if !bytes.Equal(raw, canonical) {
		return nil, nil, "", errors.New("runtime seal: noncanonical document")
	}
	return &seal, canonical, identity, nil
}

func runtimeDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// VerifyBaseManifest binds exact unchanged publisher bytes and their architecture.
// It does not authenticate custody or transform the publisher's manifest.
func (seal *RuntimeSeal) VerifyBaseManifest(raw []byte) error {
	if seal == nil || runtimeDigest(raw) != seal.BaseManifestSHA256 {
		return errors.New("runtime seal: publisher manifest digest differs")
	}
	manifest, err := pkgmanifest.Read(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if manifest.Architecture != seal.Architecture {
		return errors.New("runtime seal: publisher architecture differs")
	}
	return nil
}
