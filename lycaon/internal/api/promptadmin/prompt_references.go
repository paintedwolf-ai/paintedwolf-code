package promptadmin

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/internal/visual"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *References) ReferenceDeps(ctx context.Context, sess *wire.Session) promptattach.ReferenceDeps {
	sessionID := ""
	projectID := ""
	if sess != nil {
		sessionID = sess.ID
		projectID = strings.TrimSpace(sess.ProjectID)
	}
	return promptattach.ReferenceDeps{
		SessionProjectID: projectID,
		ResolvePath: func(refProjectID, rootID, path string) (string, wire.NavigationTarget, error) {
			return s.resolveReferencePath(ctx, refProjectID, rootID, path)
		},
		LookupArtifact: func(artifactID string) (string, error) {
			return s.lookupReferenceArtifact(ctx, sessionID, artifactID)
		},
		LookupEvidence: func(refProjectID, sourceRef, hitKind, hitSessionID string) (promptattach.ReferenceEvidence, error) {
			return s.lookupReferenceEvidence(ctx, refProjectID, sourceRef, hitKind, hitSessionID)
		},
		ReadLines: func(refProjectID, rootID, path string, startLine, endLine int) (string, error) {
			return s.readReferenceLines(ctx, refProjectID, rootID, path, startLine, endLine)
		},
	}
}

// readReferenceLines prefers the open document containing the selection.
func (s *References) readReferenceLines(ctx context.Context, projectID, rootID, path string, startLine, endLine int) (string, error) {
	p, err := s.Projects.Get(ctx, strings.TrimSpace(projectID))
	if err != nil || p == nil {
		return "", fmt.Errorf("project not found")
	}
	roots := project.RootRefsFrom(p)
	abs, root, err := projectroot.ResolveAbs(roots, rootID, path)
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root.Path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if s.sourceEditor.EditorDocuments != nil {
		document, open, draftErr := s.sourceEditor.EditorDocuments.OpenText(ctx, p.ID, p.SourceBranch, root.ID, rel)
		if draftErr == nil && open {
			return promptattach.SliceLines(document.Draft, startLine, endLine)
		}
	}
	read, err := projectsource.ReadProjectSource(p, projectsource.SourceReadRequest{Path: rel, RootID: root.ID})
	if err != nil {
		return "", err
	}
	if read.Binary || read.OverLimit || read.Encoding == "" {
		return "", fmt.Errorf("%s is not text the prompt can carry", rel)
	}
	return promptattach.SliceLines(read.Content, startLine, endLine)
}

// resolveReferencePath jails a reference and names the root it landed in, so
// the stored part carries identity a client can resolve without guessing.
func (s *References) resolveReferencePath(ctx context.Context, projectID, rootID, path string) (string, wire.NavigationTarget, error) {
	p, err := s.Projects.Get(ctx, strings.TrimSpace(projectID))
	if err != nil || p == nil {
		return "", wire.NavigationTarget{}, fmt.Errorf("project not found")
	}
	roots := project.RootRefsFrom(p)
	if len(roots) == 0 {
		return "", wire.NavigationTarget{}, fmt.Errorf("project has no attached folders")
	}
	abs, root, err := projectroot.ResolveAbs(roots, rootID, path)
	if err != nil {
		return "", wire.NavigationTarget{}, err
	}
	primary, _ := projectroot.PrimaryRoot(roots)
	rel := projectroot.Qualify(primary, root, abs)
	if strings.TrimSpace(rel) == "" {
		return "", wire.NavigationTarget{}, projectroot.ErrPathEscape
	}
	return rel, wire.NavigationTarget{ProjectID: p.ID, RootID: root.ID, Path: projectroot.ScopeRel(root, abs)}, nil
}

// lookupReferenceArtifact validates reused artifacts before they become provider image blocks.
func (s *References) lookupReferenceArtifact(ctx context.Context, sessionID, artifactID string) (string, error) {
	root := session.RootSessionID(ctx, s.Store, sessionID)
	id, res := visual.ResolveRef(ctx, s.VisualStore, root, artifactID)
	if !res.IsPresent() {
		return "", fmt.Errorf("artifact %s: %s", artifactID, res.Note())
	}
	if _, err := providerwire.ValidateWireImage(res.Bytes(), res.Meta().Mime, s.Caps.Transport.MaxImage); err != nil {
		return "", err
	}
	return id, nil
}

func (s *References) lookupReferenceEvidence(ctx context.Context, projectID, sourceRef, hitKind, sessionID string) (promptattach.ReferenceEvidence, error) {
	sqlStore, ok := s.Store.(*store.SQL)
	if !ok || sqlStore == nil || sqlStore.DB() == nil {
		return promptattach.ReferenceEvidence{}, search.ErrEvidenceNotFound
	}
	row, err := search.LookupBySourceRef(ctx, sqlStore.DB(), projectID, sourceRef, hitKind, sessionID)
	if err != nil {
		return promptattach.ReferenceEvidence{}, err
	}
	sourceContext, err := sourceref.ReadContext(ctx, sqlStore.DB(), row.SessionID, row.MessageID)
	if err != nil {
		return promptattach.ReferenceEvidence{}, err
	}
	return promptattach.ReferenceEvidence{Snippet: row.Snippet, HitKind: row.HitKind, SessionID: row.SessionID, SourceContext: sourceContext}, nil
}
