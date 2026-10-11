package checkpointcontrol

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/project"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/sourcerewind"
)

func (m *Rewinds) SetSourceRewinds(service *sourcerewind.Service) { m.sourceRewinds = service }
func (m *Rewinds) SourceRewinds() *sourcerewind.Service           { return m.sourceRewinds }

func (m *Rewinds) rewindProject(ctx context.Context, sessionID string) (*project.Project, error) {
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if m.projects == nil {
		return nil, fmt.Errorf("rewind project registry unavailable")
	}
	p, err := m.projects.Get(ctx, sess.ProjectID)
	if err != nil {
		return nil, err
	}
	binding, bound, err := m.workspace.Binding(ctx, sess)
	if err != nil {
		return nil, err
	}
	if bound {
		if _, err := m.workspace.Roots(ctx, sess); err != nil {
			return nil, err
		}
		p = project.WithWorktree(p, binding)
	}
	return p, nil
}

func (m *Rewinds) prepareSourceRewindJournal(ctx context.Context, cp *sessioncheckpoint.Store, man *sessioncheckpoint.Manifest, id, sessionID string, anchors []string) (*sessioncheckpoint.Journal, error) {
	p, err := m.rewindProject(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	source, err := m.sourceRewinds.Prepare(ctx, p, sessionID, anchors)
	if err != nil {
		return nil, err
	}
	if err := sessioncheckpoint.CheckCoverage(ctx, m.store, p, sessiontree.RootID(ctx, m.store, sessionID), anchors, source); err != nil {
		return nil, err
	}
	person, err := people.Deciding(ctx)
	if err != nil {
		return nil, err
	}
	source.PersonID = person.ID
	j, err := sessioncheckpoint.NewJournal(cp, man, id, sessionID, source)
	if err != nil {
		return nil, err
	}
	if err := m.bindSourceRewindJournal(ctx, j); err != nil {
		return nil, err
	}
	if err := j.Write(); err != nil {
		return nil, err
	}
	return j, nil
}

func (m *Rewinds) bindSourceRewindJournal(ctx context.Context, j *sessioncheckpoint.Journal) error {
	if j.Source == nil {
		return fmt.Errorf("rewind source plan missing")
	}
	if m.sourceRewinds == nil {
		return fmt.Errorf("source rewind service unavailable")
	}
	p, err := m.rewindProject(ctx, j.TranscriptID)
	if err != nil {
		return err
	}
	return j.Bind(ctx, m.sourceRewinds, p)
}
