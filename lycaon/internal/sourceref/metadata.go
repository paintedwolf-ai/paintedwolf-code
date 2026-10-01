package sourceref

import (
	"database/sql"
	"fmt"
	"path"
	"strings"

	"github.com/lycaon/lycaon/internal/jsonblob"
	"github.com/lycaon/lycaon/pkg/api"
)

const navigationRefsBlobVersion = 1

type Metadata struct {
	Refs    []api.NavigationReference `json:"refs"`
	Context api.SourceContext         `json:"context"`
}

func EncodeMetadata(refs []api.NavigationReference, sourceContext *api.SourceContext) (sql.NullString, error) {
	if refs == nil && sourceContext == nil {
		return sql.NullString{}, nil
	}
	if err := validateNavigationRefs(refs); err != nil {
		return sql.NullString{}, err
	}
	if refs == nil {
		refs = []api.NavigationReference{}
	}
	context := api.SourceContext{Locations: []api.NavigationTarget{}}
	if sourceContext != nil {
		context = *sourceContext
	}
	if context.Locations == nil {
		context.Locations = []api.NavigationTarget{}
	}
	if err := validateContext(context); err != nil {
		return sql.NullString{}, err
	}
	raw, err := jsonblob.Marshal(Metadata{Refs: refs, Context: context}, navigationRefsBlobVersion)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("marshal navigation references: %w", err)
	}
	return sql.NullString{String: string(raw), Valid: true}, nil
}

func DecodeMetadata(raw sql.NullString) (Metadata, error) {
	if !raw.Valid {
		return Metadata{}, nil
	}
	var payload Metadata
	if err := jsonblob.Unmarshal([]byte(raw.String), &payload, navigationRefsBlobVersion); err != nil {
		return Metadata{}, fmt.Errorf("decode navigation metadata: %w", err)
	}
	if payload.Refs == nil || payload.Context.Locations == nil {
		return Metadata{}, fmt.Errorf("navigation metadata lacks collections")
	}
	if err := validateNavigationRefs(payload.Refs); err != nil {
		return Metadata{}, err
	}
	if err := validateContext(payload.Context); err != nil {
		return Metadata{}, err
	}
	return payload, nil
}

func validateContext(context api.SourceContext) error {
	if len(context.Locations) > api.MaxSourceContextLocations {
		return fmt.Errorf("source context exceeds limit")
	}
	seen := make(map[string]bool, len(context.Locations))
	for _, target := range context.Locations {
		if !Valid(target) || seen[Key(target)] {
			return fmt.Errorf("source context has invalid or duplicate location")
		}
		seen[Key(target)] = true
	}
	return nil
}

func validateNavigationRefs(refs []api.NavigationReference) error {
	if len(refs) > api.MaxMessageNavigationRefs {
		return fmt.Errorf("navigation references exceed limit: %d", len(refs))
	}
	ids := map[string]bool{}
	for i, ref := range refs {
		if strings.TrimSpace(ref.Mention) == "" || len(ref.Mention) > 4096 || !ValidNavigationPath(ref.Path) || ref.ProjectID == "" {
			return fmt.Errorf("navigation reference %d has invalid address", i)
		}
		if ref.ID == "" || len(ref.ID) > 128 {
			return fmt.Errorf("navigation reference %d has invalid id", i)
		}
		switch ref.Syntax {
		case "code", "text", "link", "fence":
		default:
			return fmt.Errorf("navigation reference %d has invalid syntax", i)
		}
		if ids[ref.ID] {
			return fmt.Errorf("navigation reference %d duplicates reference id", i)
		}
		ids[ref.ID] = true
		switch ref.Status {
		case api.NavigationPending, api.NavigationAmbiguous, api.NavigationMissing, api.NavigationUnavailable:
		case api.NavigationResolved:
			if ref.RootID == "" || (ref.EntryKind != api.NavigationEntryKindFile && ref.EntryKind != api.NavigationEntryKindFolder) {
				return fmt.Errorf("navigation reference %d lacks a target", i)
			}
		default:
			return fmt.Errorf("navigation reference %d has invalid status", i)
		}
		if len(ref.Candidates) > 32 {
			return fmt.Errorf("navigation reference %d exceeds candidate page", i)
		}
		for _, target := range ref.Candidates {
			if target.ProjectID != ref.ProjectID || target.RootID == "" || !ValidNavigationPath(target.Path) || (target.EntryKind != api.NavigationEntryKindFile && target.EntryKind != api.NavigationEntryKindFolder) {
				return fmt.Errorf("navigation reference %d has invalid candidate", i)
			}
		}
	}
	return nil
}

// ValidNavigationPath is the single rule for stored navigation paths.
func ValidNavigationPath(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && trimmed == value && !strings.ContainsAny(trimmed, "\\\x00\r\n") && !strings.HasPrefix(trimmed, "/") && path.Clean(trimmed) == trimmed && trimmed != "." && trimmed != ".." && !strings.HasPrefix(trimmed, "../")
}
