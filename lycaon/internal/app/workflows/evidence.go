package workflows

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/search"
)

func (r *Runtime) bindEvidence(deps Dependencies) {
	evidenceStore := inspector.NewJSONLStore(inspector.DefaultEvidenceDir)
	simpleInspector := inspector.NewSimpleInspector(evidenceStore)
	simpleInspector.ProjectDir = func(c context.Context, runID string) (string, error) {
		runID = strings.TrimSpace(runID)
		if runID == "" {
			return "", nil
		}
		if r.Store != nil && r.Store.Runs != nil {
			if wfRun, err := r.Store.Runs.Get(c, runID); err == nil && wfRun != nil && deps.Sessions != nil {
				if sess, sErr := deps.Sessions.Get(c, wfRun.SessionID); sErr == nil && sess != nil && strings.TrimSpace(sess.ProjectID) != "" {
					return project.EnsureHostDataDir(deps.DataDir, sess.ProjectID)
				}
			}
		}
		if deps.Sessions != nil {
			if sess, err := deps.Sessions.Get(c, runID); err == nil && sess != nil && strings.TrimSpace(sess.ProjectID) != "" {
				return project.EnsureHostDataDir(deps.DataDir, sess.ProjectID)
			}
		}
		if deps.DelegationStore == nil {
			return "", nil
		}
		rec, err := deps.DelegationStore.Get(c, runID)
		if err != nil || rec == nil {
			return "", err
		}
		return project.EnsureHostDataDir(deps.DataDir, rec.ProjectID)
	}
	r.Manager.Verdicts.EvidenceStore = evidenceStore
	r.Manager.SetEvidenceProjectDir(func(c context.Context, sessionID string) (string, error) {
		if deps.Sessions == nil {
			return "", nil
		}
		sess, err := deps.Sessions.Get(c, sessionID)
		if err != nil || sess == nil {
			return "", err
		}
		if strings.TrimSpace(sess.ProjectID) == "" {
			return "", nil
		}
		return project.EnsureHostDataDir(deps.DataDir, sess.ProjectID)
	})
	r.Manager.Verdicts.OnGateEvidencePersisted = func(c context.Context, sessionID, workflowRunID string, rec evidence.Record) {
		projectID, err := search.ResolveProjectIDForSession(c, deps.Database, sessionID)
		if err != nil || projectID == "" {
			return
		}
		_ = search.ProjectGateEvidenceComplete(c, deps.Database, search.ProjectGateEvidenceInput{
			ProjectID:     projectID,
			SessionID:     sessionID,
			WorkflowRunID: workflowRunID,
			Record:        rec,
		})
	}

	r.Evidence = evidenceStore
	r.Inspector = simpleInspector
}
