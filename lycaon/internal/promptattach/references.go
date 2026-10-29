package promptattach

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/promptattach/attacherr"
	"github.com/lycaon/lycaon/internal/promptattach/format"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/pkg/api"
)

// Host-defined reference hint prefixes.
const (
	PathFileHintPrefix   = "[User attached file: "
	PathFolderHintPrefix = "[User attached folder: "
	PathImageHintPrefix  = "[User attached image: "
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
	// ReadImage reads an image if the path resolves to an image file under project roots.
	ReadImage func(projectID, rootID, path string) (bytes []byte, mime string, err error)
	// MaxImageBytes is the maximum image size admitted.
	MaxImageBytes int64
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

// ReferenceResult is framed reference parts plus artifact ids to re-link and attached images.
type ReferenceResult struct {
	Parts       []FramedPart
	ArtifactIDs []string
	Images      []InlineImage
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
		Images:      make([]InlineImage, 0),
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
			part, img, err := ingestPathFileRef(deps, previewBudget, i, *ref.PathFile)
			if err != nil {
				return ReferenceResult{}, err
			}
			out.Parts = append(out.Parts, part)
			if img != nil {
				img.PartIndex = len(out.Parts) - 1
				out.Images = append(out.Images, *img)
			}
		case ref.PathFolder != nil:
			part, err := ingestPathFolderRef(deps, i, *ref.PathFolder)
			if err != nil {
				return ReferenceResult{}, err
			}
			out.Parts = append(out.Parts, part)
		case ref.ExternalFile != nil:
			part, img, err := ingestExternalFileRef(deps, previewBudget, i, *ref.ExternalFile)
			if err != nil {
				return ReferenceResult{}, err
			}
			out.Parts = append(out.Parts, part)
			if img != nil {
				img.PartIndex = len(out.Parts) - 1
				out.Images = append(out.Images, *img)
			}
		case ref.ExternalFolder != nil:
			part, err := ingestExternalFolderRef(deps, i, *ref.ExternalFolder)
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

func ingestPathFileRef(deps ReferenceDeps, previewBudget *TurnPreviewBudget, i int, ref api.PromptReferencePathFilePart) (FramedPart, *InlineImage, error) {
	rawPath := strings.TrimSpace(ref.Path)
	if rawPath == "" {
		return FramedPart{}, nil, attacherr.Unsupported(fmt.Sprintf("references[%d]: path required", i))
	}
	if deps.ResolvePath == nil {
		return FramedPart{}, nil, attacherr.OutOfJail(fmt.Sprintf("references[%d]: path resolver not configured", i))
	}
	rel, location, err := deps.ResolvePath(strings.TrimSpace(ref.ProjectID), strings.TrimSpace(ref.RootID), rawPath)
	if err != nil {
		msg := strings.TrimSpace(err.Error())
		if msg == "" {
			msg = "That path is outside your project folders."
		}
		return FramedPart{}, nil, attacherr.OutOfJail(fmt.Sprintf("references[%d]: %s", i, msg))
	}
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return FramedPart{}, nil, attacherr.OutOfJail(fmt.Sprintf("references[%d]: unresolved path", i))
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

	// If the file is an image, load it for visual perception.
	if deps.ReadImage != nil {
		if imgBytes, mime, imgErr := deps.ReadImage(strings.TrimSpace(ref.ProjectID), strings.TrimSpace(ref.RootID), rawPath); imgErr == nil && len(imgBytes) > 0 {
			hint := PathImageHintPrefix + display + "]"
			part := FramedPart{
				SourceContext: &api.SourceContext{Locations: []api.NavigationTarget{location}},
				Fence:         FormatFence(name, mime, hint, false),
				Source:        rel,
				MediaType:     mime,
				Path:          rel,
				RootID:        location.RootID,
				SizeBytes:     int64(len(imgBytes)),
				ReferenceKind: api.MessageReferenceKindPathFile,
			}
			img := InlineImage{Mime: mime, Bytes: imgBytes}
			return part, &img, nil
		}
	}

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
	return part, nil, nil
}

func ingestExternalFileRef(deps ReferenceDeps, previewBudget *TurnPreviewBudget, i int, ref api.PromptReferenceExternalFilePart) (FramedPart, *InlineImage, error) {
	rawPath := strings.TrimSpace(ref.Path)
	if rawPath == "" {
		return FramedPart{}, nil, attacherr.Unsupported(fmt.Sprintf("references[%d]: path required", i))
	}
	if !filepath.IsAbs(rawPath) {
		return FramedPart{}, nil, attacherr.OutOfJail(fmt.Sprintf("references[%d]: external path must be absolute", i))
	}
	abs := filepath.Clean(fspath.CanonicalPath(rawPath))
	if confine.ControlPlanePathDenied(abs, false, "", "") {
		return FramedPart{}, nil, attacherr.OutOfJail(fmt.Sprintf("references[%d]: %s is a protected path", i, abs))
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return FramedPart{}, nil, attacherr.NotFound(fmt.Sprintf("references[%d]: %s does not exist", i, abs))
		}
		return FramedPart{}, nil, attacherr.Unsupported(fmt.Sprintf("references[%d]: stat %s: %v", i, abs, err))
	}
	if info.IsDir() {
		return FramedPart{}, nil, attacherr.Unsupported(fmt.Sprintf("references[%d]: %s is a directory; use external-folder", i, abs))
	}

	display := abs
	if start := ref.StartLine; start >= 1 {
		end := ref.EndLine
		if end < start {
			end = start
		}
		if end == start {
			display = fmt.Sprintf("%s:%d", abs, start)
		} else {
			display = fmt.Sprintf("%s:%d-%d", abs, start, end)
		}
	}
	name := filepath.Base(abs)
	if !info.Mode().IsRegular() {
		return FramedPart{}, nil, attacherr.Unsupported(fmt.Sprintf("references[%d]: a regular file is required", i))
	}

	peek := make([]byte, format.PeekBytes)
	f, err := fseffect.OpenRead(fseffect.Location{Root: filepath.Dir(abs), Rel: filepath.Base(abs)})
	if err != nil {
		return FramedPart{}, nil, attacherr.Unsupported(fmt.Sprintf("references[%d]: open %s: %v", i, abs, err))
	}
	defer func() { _ = f.Close() }()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return FramedPart{}, nil, attacherr.Unsupported(fmt.Sprintf("references[%d]: a regular file is required", i))
	}
	n, _ := io.ReadFull(f, peek)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return FramedPart{}, nil, attacherr.Unsupported(err.Error())
	}
	detected := format.Detect(name, "", peek[:n])

	if detected.Kind == format.KindImage {
		maxImage := deps.MaxImageBytes
		if maxImage <= 0 {
			maxImage = Active().Transport.MaxImage.Int64()
		}
		if info.Size() > maxImage {
			return FramedPart{}, nil, attacherr.TooLarge(fmt.Sprintf("references[%d]: image %s exceeds byte cap", i, name))
		}
		rawBytes, err := io.ReadAll(io.LimitReader(f, maxImage+1))
		if err != nil {
			return FramedPart{}, nil, attacherr.Unsupported(fmt.Sprintf("references[%d]: read %s: %v", i, abs, err))
		}
		if int64(len(rawBytes)) > maxImage {
			return FramedPart{}, nil, attacherr.TooLarge(fmt.Sprintf("references[%d]: image %s exceeds byte cap", i, name))
		}
		hint := PathImageHintPrefix + display + "]"
		part := FramedPart{
			Fence:         FormatFence(name, detected.MIME, hint, false),
			Source:        abs,
			MediaType:     detected.MIME,
			Path:          abs,
			SizeBytes:     info.Size(),
			ReferenceKind: api.MessageReferenceKindExternalFile,
		}
		img := InlineImage{Mime: detected.MIME, Bytes: rawBytes}
		return part, &img, nil
	}

	hint := PathFileHintPrefix + display + "]"
	mediaType := detected.MIME
	if mediaType == "" {
		mediaType = "text/plain"
	}
	part := FramedPart{
		Fence:         FormatFence(name, mediaType, hint, false),
		Source:        abs,
		MediaType:     mediaType,
		Path:          abs,
		SizeBytes:     info.Size(),
		ReferenceKind: api.MessageReferenceKindExternalFile,
	}

	if start := ref.StartLine; start >= 1 {
		part.StartLine = start
		part.EndLine = ref.EndLine
		if part.EndLine < start {
			part.EndLine = start
		}
	}
	if previewBudget != nil && (ref.StartLine >= 1 || detected.Kind == format.KindText) {
		if fence := externalReferencePreview(f, name, mediaType, hint, info.Size(), part.StartLine, part.EndLine, previewBudget); fence != "" {
			part.Fence = fence
		}
	}

	return part, nil, nil
}

func ingestExternalFolderRef(deps ReferenceDeps, i int, ref api.PromptReferenceExternalFolderPart) (FramedPart, error) {
	rawPath := strings.TrimSpace(ref.Path)
	if rawPath == "" {
		return FramedPart{}, attacherr.Unsupported(fmt.Sprintf("references[%d]: path required", i))
	}
	if !filepath.IsAbs(rawPath) {
		return FramedPart{}, attacherr.OutOfJail(fmt.Sprintf("references[%d]: external path must be absolute", i))
	}
	abs := filepath.Clean(fspath.CanonicalPath(rawPath))
	if confine.ControlPlanePathDenied(abs, false, "", "") {
		return FramedPart{}, attacherr.OutOfJail(fmt.Sprintf("references[%d]: %s is a protected path", i, abs))
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return FramedPart{}, attacherr.NotFound(fmt.Sprintf("references[%d]: %s does not exist", i, abs))
		}
		return FramedPart{}, attacherr.Unsupported(fmt.Sprintf("references[%d]: stat %s: %v", i, abs, err))
	}
	if !info.IsDir() {
		return FramedPart{}, attacherr.Unsupported(fmt.Sprintf("references[%d]: %s is not a folder; use external-file", i, abs))
	}
	name := filepath.Base(abs)
	hint := PathFolderHintPrefix + abs + "]"
	return FramedPart{
		Fence:         FormatFence(name, "text/plain", hint, false),
		Source:        abs,
		MediaType:     "text/plain",
		Path:          abs,
		ReferenceKind: api.MessageReferenceKindExternalFolder,
	}, nil
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
