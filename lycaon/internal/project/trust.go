package project

import (
	"errors"
	"reflect"
	"slices"
	"strings"
)

// SeenRecord retains both sides of an acknowledged trust comparison.
type SeenRecord struct {
	Stamp       string          `json:"stamp"`
	Files       []TrustReadFile `json:"files"`
	BeforeFiles []TrustReadFile `json:"before_files"`
	ReadAt      string          `json:"read_at"`
}

// TrustReadFile is a retained review baseline, including detached roots.
type TrustReadFile struct {
	RootID    string `json:"root_id"`
	RootLabel string `json:"root_label"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	SHA256    string `json:"sha256"`
}

// TrustEnabled reports whether a project has this surface on. Missing means on.
func TrustEnabled(p Project, surfaceID string) bool {
	if p.TrustEnabled == nil {
		return true
	}
	if v, ok := p.TrustEnabled[surfaceID]; ok {
		return v
	}
	return true
}

func MergeTrustEnabled(existing map[string]bool, updates map[string]bool) map[string]bool {
	out := make(map[string]bool, len(existing)+len(updates))
	for k, v := range existing {
		out[k] = v
	}
	for k, v := range updates {
		out[k] = v
	}
	return out
}

// SurfaceSeen reports whether the tree matches the last-read version.
func SurfaceSeen(p Project, surfaceID, currentStamp string) bool {
	if strings.TrimSpace(currentStamp) == "" {
		return p.TrustSeen[surfaceID].Stamp == ""
	}
	return p.TrustSeen[surfaceID].Stamp == currentStamp
}

func cloneTrustReadBaseline(in map[string]SeenRecord) map[string]SeenRecord {
	out := make(map[string]SeenRecord, len(in))
	for id, record := range in {
		if strings.TrimSpace(record.Stamp) != "" || len(record.BeforeFiles) > 0 {
			out[id] = cloneSeenRecord(record)
		}
	}
	return out
}

func cloneSeenRecord(in SeenRecord) SeenRecord {
	in.Files = slices.Clone(in.Files)
	in.BeforeFiles = slices.Clone(in.BeforeFiles)
	return in
}

var ErrTrustReviewChanged = errors.New("project trust changed while opening the review; open it again")

func seenSummaries(in map[string]SeenRecord) map[string]SeenRecord {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]SeenRecord, len(in))
	for id, record := range in {
		record.Files = nil
		record.BeforeFiles = nil
		out[id] = record
	}
	return out
}

func sameSeenRecords(a, b map[string]SeenRecord) bool {
	return reflect.DeepEqual(seenSummaries(a), seenSummaries(b))
}
