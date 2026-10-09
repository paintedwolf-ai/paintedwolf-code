package session

import (
	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
)

func (m *Manager) SetFindingsStore(store findings.Store) { m.Workers.Notes.SetFindings(store) }
func (m *Manager) SetPeerRejectionFeed(feed *workeroutcomes.PeerRejectionFeed) {
	m.Workers.Rejections = feed
	m.Workers.Notes.SetPeers(feed)
	if feed != nil {
		feed.SetSources(m.store, m.workerQueue)
	}
}
