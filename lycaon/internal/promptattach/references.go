package promptattach

import (
	"fmt"
	"path"
	"strings"

	"github.com/lycaon/lycaon/internal/promptattach/attacherr"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/pkg/api"
)

// Host-defined reference hint prefixes.
const (
	PathFileHintPrefix   = "[User attached file: "
	PathFolderHintPrefix = "[User attached folder: "
	SearchHitHintPrefix  = "[User attached search result: "
)

// ReferenceDeps supplies reference lookups.
type ReferenceDeps struct {
	// SessionProjectID scopes every reference.
	SessionProjectID string
	// ResolvePath returns the displayed path and its canonical source address.
	ResolvePath func(projectID, rootID, path string) (displayPath string, location api.NavigationTarget, err error)
	// LookupArtifact returns a tree-scoped artifact ID.
	LookupArtifact func(artifactID string) (canonicalID string, err error)
	// LookupEvidence returns stored search-hit content and coordinates.
	LookupEvidence func(projectID, sourceRef, hitKind, sessionID string) (ReferenceEvidence, error)
	// ReadLines returns a referenced line range as the person sees it: the
	// open editor text when the file is open, otherwise the file.
	ReadLines func(projectID, rootID, path string, startLine, endLine int) (string, error)
}

type ReferenceEvidence struct {
	Snippet       string
	HitKind       string
	SessionID     string
	SourceContext *api.SourceContext
}

// SliceLines returns lines startLine through endLine of text, 1-based and
// inclusive, clamping the end to the last line.
func SliceLines(text string, startLine, endLine int) (string, error) {
	if startLine < 1 {
		return "", fmt.Errorf("line %d is before the first line", startLine)
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if startLine > len(lines) {
		return "", fmt.Errorf("line %d is past the end of the file (%d lines)", startLine, len(lines))
	}
	if endLine < startLine {
		endLine = startLine
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	return strings.Join(lines[startLine-1:endLine], "\n"), nil
}

// ReferenceResult is framed reference parts plus artifact ids to re-link.
type ReferenceResult struct {
	Parts       []FramedPart
	ArtifactIDs []string
}

// Fences returns model-facing fence strings for the ingested references.
func (r ReferenceResult) Fences() []string { return Fences(r.Parts) }

// IngestReferences resolves typed references for one prompt.
func IngestReferences(deps ReferenceDeps, previewBudget *TurnPreviewBudget, refs []api.PromptReferencePart) (ReferenceResult, error) {
	if len(refs) == 0 {
		return ReferenceResult{}, nil
	}
	out := ReferenceResult{
		Parts:       make([]FramedPart, 0, len(refs)),
		ArtifactIDs: make([]string, 0),
	}
	sessionProject := strings.TrimSpace(deps.SessionProjectID)

	for i, ref := range refs {
		if err := ref.Validate(); err != nil {
			return ReferenceResult{}, attacherr.Unsupported(fmt.Sprintf("references[%d]: %v", i, err))
		}
		projectID := strings.TrimSpace(ref.ProjectID())
		if projectID == "" {
			return ReferenceResult{}, attacherr.Unsupported(fmt.Sprintf("references[%d]: project_id required", i))
		}
		if sessionProject != "" && projectID != sessionProject {
			return ReferenceResult{}, attacherr.OutOfJail(fmt.Sprintf("references[%d]: project_id does not match the session project", i))
		}

		switch {
		case ref.PathFile != nil:
			part, err := ingestPathFileRef(deps, previewBudget, i, *ref.PathFile)
			if err != nil {
				return ReferenceResult{}, err
			}
			out.Parts = append(out.Parts, part)
		case ref.PathFolder != nil:
			part, err := ingestPathFolderRef(deps, i, *ref.PathFolder)
			if err != nil {
				return ReferenceResult{}, err
			}
			out.Parts = append(out.Parts, part)
		case ref.Artifact != nil:
			id, err := ingestArtifactRef(deps, i, *ref.Artifact)
			if err != nil {
				return ReferenceResult{}, err
			}
			out.ArtifactIDs = append(out.ArtifactIDs, id)
		case ref.SearchHit != nil:
			part, err := ingestSearchHitRef(deps, previewBudget, i, *ref.SearchHit)
			if err != nil {
				return ReferenceResult{}, err
			}
			out.Parts = append(out.Parts, part)

		default:
			return ReferenceResult{}, attacherr.Unsupported(fmt.Sprintf("references[%d]: invalid reference variant", i))
		}
	}
	return out, nil
}

func ingestPathFileRef(deps ReferenceDeps, previewBudget *TurnPreviewBudget, i int, ref api.PromptReferencePathFilePart) (FramedPart, error) {
	rawPath := strings.TrimSpace(ref.Path)
	if rawPath == "" {
		return FramedPart{}, attacherr.Unsupported(fmt.Sprintf("references[%d]: path required", i))
	}
	if deps.ResolvePath == nil {
		return FramedPart{}, attacherr.OutOfJail(fmt.Sprintf("references[%d]: path resolver not configured", i))
	}
	rel, location, err := deps.ResolvePath(strings.TrimSpace(ref.ProjectID), strings.TrimSpace(ref.RootID), rawPath)
	if err != nil {
		msg := strings.TrimSpace(err.Error())
		if msg == "" {
			msg = "That path is outside your project folders."
		}
		return FramedPart{}, attacherr.OutOfJail(fmt.Sprintf("references[%d]: %s", i, msg))
	}
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return FramedPart{}, attacherr.OutOfJail(fmt.Sprintf("references[%d]: unresolved path", i))
	}
	rel = path.Clean(strings.ReplaceAll(rel, "\\", "/"))
	display := rel
	if start := ref.StartLine; start >= 1 {
		end := ref.EndLine
		if end < start {
			end = start
		}
		if end == start {
			display = fmt.Sprintf("%s:%d", rel, start)
		} else {
			display = fmt.Sprintf("%s:%d-%d", rel, start, end)
		}
	}
	hint := PathFileHintPrefix + display + "]"
	name := path.Base(rel)
	if name == "." || name == "/" || name == "" {
		name = rel
	}
	location.EntryKind = api.NavigationEntryKindFile
	part := FramedPart{
		SourceContext: &api.SourceContext{Locations: []api.NavigationTarget{location}},
		Fence:         FormatFence(name, "text/plain", hint, false),
		Source:        rel,
		MediaType:     "text/plain",
		Path:          rel,
		RootID:        location.RootID,
		ReferenceKind: api.MessageReferenceKindPathFile,
	}
	// Source stays unadorned; bounds remain structured.
	if start := ref.StartLine; start >= 1 {
		part.StartLine = start
		part.EndLine = ref.EndLine
		if part.EndLine < start {
			part.EndLine = start
		}
		// Line references include the selected text; whole-file references defer reading.
		if deps.ReadLines != nil && previewBudget != nil {
			text, readErr := deps.ReadLines(strings.TrimSpace(ref.ProjectID), strings.TrimSpace(ref.RootID), rawPath, part.StartLine, part.EndLine)
			if readErr == nil && text != "" {
				body := hint + "\n" + text
				part.Fence = frameSnippet(name, "text/plain", body, previewBudget.claimBody(int64(len(body))))
			}
		}
	}
	return part, nil
}

func ingestPathFolderRef(deps ReferenceDeps, i int, ref api.PromptReferencePathFolderPart) (FramedPart, error) {
	rawPath := strings.TrimSpace(ref.Path)
	if rawPath == "" {
		return FramedPart{}, attacherr.Unsupported(fmt.Sprintf("references[%d]: path required", i))
	}
	if deps.ResolvePath == nil {
		return FramedPart{}, attacherr.OutOfJail(fmt.Sprintf("references[%d]: path resolver not configured", i))
	}
	rel, location, err := deps.ResolvePath(strings.TrimSpace(ref.ProjectID), strings.TrimSpace(ref.RootID), rawPath)
	if err != nil {
		msg := strings.TrimSpace(err.Error())
		if msg == "" {
			msg = "That path is outside your project folders."
		}
		return FramedPart{}, attacherr.OutOfJail(fmt.Sprintf("references[%d]: %s", i, msg))
	}
	rel = path.Clean(strings.ReplaceAll(strings.TrimSpace(rel), "\\", "/"))
	if rel == "" {
		return FramedPart{}, attacherr.OutOfJail(fmt.Sprintf("references[%d]: unresolved path", i))
	}
	name := path.Base(rel)
	if name == "." || name == "/" || name == "" {
		name = rel
	}
	location.EntryKind = api.NavigationEntryKindFolder
	return FramedPart{
		SourceContext: &api.SourceContext{Locations: []api.NavigationTarget{location}},
		Fence:         FormatFence(name, "text/plain", PathFolderHintPrefix+rel+"]", false),
		Source:        rel,
		MediaType:     "text/plain",
		Path:          rel,
		RootID:        location.RootID,
		ReferenceKind: api.MessageReferenceKindPathFolder,
	}, nil
}

func ingestArtifactRef(deps ReferenceDeps, i int, ref api.PromptReferenceArtifactPart) (string, error) {
	artifactID := strings.TrimSpace(ref.ArtifactID)
	if artifactID == "" {
		return "", attacherr.Unsupported(fmt.Sprintf("references[%d]: artifact_id required", i))
	}
	if deps.LookupArtifact == nil {
		return "", attacherr.Unsupported(fmt.Sprintf("references[%d]: artifact store not configured", i))
	}
	id, err := deps.LookupArtifact(artifactID)
	if err != nil || strings.TrimSpace(id) == "" {
		return "", attacherr.Unsupported(fmt.Sprintf("references[%d]: artifact not found", i))
	}
	return strings.TrimSpace(id), nil
}

func ingestSearchHitRef(deps ReferenceDeps, previewBudget *TurnPreviewBudget, i int, ref api.PromptReferenceSearchHitPart) (FramedPart, error) {
	sourceRef := strings.TrimSpace(ref.SourceRef)
	if sourceRef == "" {
		return FramedPart{}, attacherr.Unsupported(fmt.Sprintf("references[%d]: source_ref required", i))
	}
	if deps.LookupEvidence == nil {
		return FramedPart{}, attacherr.Unsupported(fmt.Sprintf("references[%d]: evidence index not configured", i))
	}
	evidence, err := deps.LookupEvidence(
		strings.TrimSpace(ref.ProjectID),
		sourceRef,
		strings.TrimSpace(ref.HitKind),
		strings.TrimSpace(ref.SessionID),
	)
	if err != nil {
		return FramedPart{}, attacherr.Unsupported(fmt.Sprintf("references[%d]: search hit not found", i))
	}
	hitKind := strings.TrimSpace(evidence.HitKind)
	if hitKind == "" {
		hitKind = strings.TrimSpace(ref.HitKind)
	}
	if hitKind == "" {
		hitKind = "search"
	}
	hint := SearchHitHintPrefix + hitKind + " " + sourceRef + "]"
	body := hint
	if snip := strings.TrimSpace(evidence.Snippet); snip != "" {
		body = hint + "\n" + snip
	}
	part := FramedPart{
		Fence:           frameSnippet(sourceRef, "text/plain", body, previewBudget.claimBody(int64(len(body)))),
		Source:          sourceRef,
		MediaType:       "text/plain",
		ReferenceKind:   api.MessageReferenceKindSearchHit,
		HitKind:         hitKind,
		SourceRef:       sourceRef,
		SourceSessionID: strings.TrimSpace(evidence.SessionID),
	}
	part.SourceContext = sourceref.Mentioned(evidence.SourceContext, part.Fence)
	return part, nil
}

func (r ReferenceResult) SourceContext() *api.SourceContext {
	if len(r.Parts) == 0 {
		return nil
	}
	contexts := make([]*api.SourceContext, 0, len(r.Parts))
	for _, part := range r.Parts {
		contexts = append(contexts, sourceref.Mentioned(part.SourceContext, part.Fence))
	}
	return sourceref.Merge(contexts...)
}
