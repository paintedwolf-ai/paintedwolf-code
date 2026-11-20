package tooloutput

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
)

const (
	// PromoteSpillDir is the subdirectory under a project's host data dir for
	// overlay promote preview spills.
	PromoteSpillDir = "promote-spills"
	// ToolOutputSpillDir stores oversized tool output under project host data.
	ToolOutputSpillDir = "tool-output"
	// AttachmentSpillDir is the shared prompt-attachment host-data directory.
	AttachmentSpillDir = "prompt-attachments"
	// DefaultMaxSpillFileBytes is the hard ceiling for one spill file on disk.
	DefaultMaxSpillFileBytes = 64 << 20 // 64 MiB
	// overlayToolResultFloorBytes is the minimum byte budget for preview/promote overlay tools.
	overlayToolResultFloorBytes = 512 << 10
)

// IsAgentWireSpillRel reports whether rel is a host-data-relative spill path
// safe to advertise on the agent wire and remap via ResolveRead.
func IsAgentWireSpillRel(rel string) bool {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if rel == "" || sandbox.HasParentTraversal(rel) {
		return false
	}
	for _, prefix := range []string{
		ToolOutputSpillDir + "/",
		PromoteSpillDir + "/",
		AttachmentSpillDir + "/",
	} {
		if strings.HasPrefix(rel, prefix) && len(rel) > len(prefix) {
			return true
		}
	}
	return false
}

// IsBlobstoreCompressedRel reports whether rel is under a host-data directory
// blobstore.Store writes (zstd on disk). PromoteSpillDir is written through
// fseffect.Replace, so it is excluded.
func IsBlobstoreCompressedRel(rel string) bool {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	for _, prefix := range []string{
		ToolOutputSpillDir + "/",
		AttachmentSpillDir + "/",
	} {
		if strings.HasPrefix(rel, prefix) && len(rel) > len(prefix) {
			return true
		}
	}
	return false
}

// AgentWireSpillScopeRel maps a model path to an allowlisted relative path.
func AgentWireSpillScopeRel(hostDataDir, modelPath string) (scopeRel string, ok bool) {
	hostDataDir = strings.TrimSpace(hostDataDir)
	modelPath = strings.TrimSpace(modelPath)
	if hostDataDir == "" || modelPath == "" {
		return "", false
	}
	host := filepath.Clean(hostDataDir)
	var abs string
	if filepath.IsAbs(modelPath) {
		abs = filepath.Clean(modelPath)
	} else {
		rel := filepath.ToSlash(filepath.Clean(filepath.FromSlash(modelPath)))
		if !IsAgentWireSpillRel(rel) {
			return "", false
		}
		abs = filepath.Join(host, filepath.FromSlash(rel))
	}
	rel, err := filepath.Rel(host, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	scopeRel = filepath.ToSlash(rel)
	if !IsAgentWireSpillRel(scopeRel) {
		return "", false
	}
	return scopeRel, true
}

// EffectiveMaxSpillFileBytes returns maxSpillBytes when positive, else DefaultMaxSpillFileBytes.
func EffectiveMaxSpillFileBytes(maxSpillBytes int) int {
	if maxSpillBytes > 0 {
		return maxSpillBytes
	}
	return DefaultMaxSpillFileBytes
}

// SpillFileTooLarge reports whether nbytes exceeds the spill-file cap.
func SpillFileTooLarge(nbytes, maxSpillBytes int) bool {
	return nbytes > EffectiveMaxSpillFileBytes(maxSpillBytes)
}

// CapSpillBytes returns the first maxSpillBytes of opaque content for on-disk spill writes.
func CapSpillBytes(content string, maxSpillBytes int) string {
	cap := EffectiveMaxSpillFileBytes(maxSpillBytes)
	if len(content) <= cap {
		return content
	}
	// The cap is a byte offset and this content is written to disk, so a
	// mid-rune cut would persist an invalid byte.
	return strings.ToValidUTF8(content[:cap], "")
}

// ToolOutputSpillRelPath returns a content-addressed host-data-relative path.
func ToolOutputSpillRelPath(content string) string {
	sum := sha256.Sum256([]byte(content))
	return ToolOutputSpillDir + "/" + hex.EncodeToString(sum[:]) + ".txt"
}

// SpillPaths returns exact content-addressed spill references written into durable text.
func SpillPaths(content string) []string {
	const digestChars = sha256.Size * 2
	prefix := ToolOutputSpillDir + "/"
	need := len(prefix) + digestChars + len(".txt")
	seen := make(map[string]struct{})
	var paths []string
	for offset := 0; offset < len(content); {
		i := strings.Index(content[offset:], prefix)
		if i < 0 {
			break
		}
		start := offset + i
		end := start + need
		if end <= len(content) {
			candidate := content[start:end]
			digest := candidate[len(prefix) : len(prefix)+digestChars]
			_, err := hex.DecodeString(digest)
			if err == nil && strings.HasSuffix(candidate, ".txt") {
				if _, exists := seen[candidate]; !exists {
					seen[candidate] = struct{}{}
					paths = append(paths, candidate)
				}
			}
		}
		offset = start + len(prefix)
	}
	return paths
}

// IsContentAddressedToolOutputSpillRel reports whether rel is one exact durable spill path.
func IsContentAddressedToolOutputSpillRel(rel string) bool {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	paths := SpillPaths(rel)
	return len(paths) == 1 && paths[0] == rel
}

// PromoteSpillRelPath returns the host-data-relative spill path for one overlay assessment.
func PromoteSpillRelPath(overlayID string) string {
	id := sanitizeSpillName(overlayID)
	if id == "" {
		id = "spill"
	}
	return PromoteSpillDir + "/" + id + ".json"
}

func sanitizeSpillName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = strings.ReplaceAll(raw, "\\", "_")
	raw = strings.ReplaceAll(raw, "/", "_")
	return raw
}

// IsOverlayPromoteTool reports coordinator overlay assess/promote tools.
func IsOverlayPromoteTool(tool string) bool {
	switch strings.TrimSpace(tool) {
	case "preview_overlay", "promote_overlay":
		return true
	default:
		return false
	}
}

// OverlayToolResultMaxBytes returns the byte cap for overlay promote tool JSON.
func OverlayToolResultMaxBytes(sessionMax int) int {
	if sessionMax <= 0 {
		return overlayToolResultFloorBytes
	}
	boosted := sessionMax * 4
	if boosted < overlayToolResultFloorBytes {
		return overlayToolResultFloorBytes
	}
	return boosted
}
