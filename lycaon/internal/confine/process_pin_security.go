package confine

import (
	"os"

	"github.com/lycaon/lycaon/internal/osprocess"
)

type fileChange struct {
	Seconds, Nanoseconds int64
	UID, GID             uint32
}

func (p *ProcessPins) permitsSecurity(before, after osprocess.ProcessSecurity, confined bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || before.RealUID != after.RealUID || before.RealGID != after.RealGID || after.SavedUID != after.EffectiveUID || after.SavedGID != after.EffectiveGID {
		return false
	}
	uid, gid := before.EffectiveUID == after.EffectiveUID, before.EffectiveGID == after.EffectiveGID
	for _, image := range p.images {
		uid = uid || image.info.Mode()&os.ModeSetuid != 0 && image.change.UID == after.EffectiveUID
		gid = gid || image.info.Mode()&os.ModeSetgid != 0 && image.change.GID == after.EffectiveGID
	}
	if !uid || !gid {
		return false
	}
	if after.Linux == nil {
		return before.Linux == nil
	}
	if before.Linux == nil {
		return false
	}
	if confined {
		return after.Linux.NoNewPrivileges && after.Linux.Effective == 0 && after.Linux.Permitted == 0 && after.Linux.Ambient == 0
	}
	return after.Linux.Permitted & ^before.Linux.Permitted == 0 && after.Linux.Effective & ^before.Linux.Effective == 0 && after.Linux.Ambient & ^before.Linux.Ambient == 0
}
