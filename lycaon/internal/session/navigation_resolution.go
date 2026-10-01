package session

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

var (
	ErrNavigationCandidateInvalid = errors.New("invalid navigation candidate")
	// ErrNavigationMessageNotProse: only visible assistant prose carries navigation references.
	ErrNavigationMessageNotProse = errors.New("message is not visible assistant prose")
)

func (m *Manager) ResolveMessageNavigation(ctx context.Context, sessionID string, req api.ResolveMessageNavigationRequest, resolve func(context.Context, *project.Project, api.Message, []api.NavigationReference) []api.NavigationReference) (api.MessageNavigationResponse, error) {
	msg, err := m.store.GetMessage(ctx, sessionID, req.MessageID)
	if err != nil {
		return api.MessageNavigationResponse{}, err
	}
	hash := store.NavigationContentHash(msg.Content)
	if req.ContentSHA256 != hash {
		return api.MessageNavigationResponse{}, store.ErrNavigationContentChanged
	}
	if msg.Role != api.MessageRoleAssistant || msg.Visibility == api.MessageVisibilityInternal {
		return api.MessageNavigationResponse{}, ErrNavigationMessageNotProse
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return api.MessageNavigationResponse{}, err
	}
	p, err := m.projects.Get(ctx, sess.ProjectID)
	if err != nil {
		return api.MessageNavigationResponse{}, err
	}
	refs := msg.NavigationRefs
	if req.ReferenceID != "" {
		refs = nil
		for _, ref := range msg.NavigationRefs {
			if ref.ID == req.ReferenceID {
				refs = append(refs, ref)
				break
			}
		}
	}
	if req.CandidateIndex != nil {
		if len(refs) != 1 || req.ReferenceID == "" || refs[0].Status != api.NavigationAmbiguous || *req.CandidateIndex < 0 || *req.CandidateIndex >= len(refs[0].Candidates) {
			return api.MessageNavigationResponse{}, ErrNavigationCandidateInvalid
		}
		ref := refs[0]
		target := ref.Candidates[*req.CandidateIndex]
		if target.ProjectID != sess.ProjectID {
			return api.MessageNavigationResponse{}, ErrNavigationCandidateInvalid
		}
		ref.ProjectID, ref.RootID, ref.Path, ref.EntryKind, ref.WorkerID = target.ProjectID, target.RootID, target.Path, target.EntryKind, target.WorkerID
		ref.Status, ref.Candidates = api.NavigationResolved, nil
		refs = []api.NavigationReference{ref}
	}
	scopedRoots, err := m.sessionRootRefs(ctx, sess)
	if err != nil {
		return api.MessageNavigationResponse{}, err
	}
	scoped := *p
	scoped.Roots = nil
	for _, root := range scopedRoots {
		scoped.Roots = append(scoped.Roots, project.Root{ID: root.ID, Label: root.Label, Path: root.Path, IsPrimary: root.IsPrimary})
	}
	if req.ReferenceID != "" {
		refs = resolve(ctx, &scoped, msg, refs)
	} else {
		positions := []int{}
		pending := []api.NavigationReference{}
		for i, ref := range refs {
			if ref.Status == api.NavigationPending {
				positions = append(positions, i)
				pending = append(pending, ref)
			}
		}
		resolved := resolve(ctx, &scoped, msg, pending)
		refs = append([]api.NavigationReference{}, refs...)
		for i, position := range positions {
			refs[position] = resolved[i]
		}
	}

	out := api.MessageNavigationResponse{ContentSHA256: hash, References: refs}
	if req.ReferenceID == "" {
		stored, err := m.store.PatchMessageNavigation(ctx, sessionID, req.MessageID, hash, msg.NavigationRefs, refs)
		if err != nil {
			return out, err
		}
		out.References = stored.NavigationRefs
	}
	return out, nil
}
