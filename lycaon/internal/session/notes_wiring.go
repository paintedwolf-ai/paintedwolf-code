package session

import (
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
)

func (m *Host) SetPeerRejectionFeed(feed *workeroutcomes.PeerRejectionFeed) {
	m.Workers.Rejections = feed
	m.Coordinator.Projection.Rejections = feed

	m.Workers.Notes.SetPeers(feed)
	if feed != nil {
		feed.SetSources(m.Coordinator.Context.Sessions, m.Coordinator.Tools.Workers)
	}
}
