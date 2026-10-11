package workeroutcomes

import (
	"strings"

	"github.com/lycaon/lycaon/internal/scopedstore"
)

type Digests struct{ pending scopedstore.LRU[string] }

func NewDigests() *Digests { return &Digests{} }
func (m *Digests) Put(jobID, digest string) {
	if m != nil {
		m.pending.Store(jobID, digest)
	}
}
func (m *Digests) Take(jobID string) string {
	jobID = strings.TrimSpace(jobID)
	if m == nil || jobID == "" {
		return ""
	}
	digest, _ := m.pending.LoadAndDelete(jobID)
	return strings.TrimSpace(digest)
}
func (m *Digests) Forget(jobID string) {
	if m != nil {
		m.pending.Delete(strings.TrimSpace(jobID))
	}
}
