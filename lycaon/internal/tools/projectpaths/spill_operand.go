package projectpaths

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/tooloutput"
)

// hostDataRootID names the virtual root of allowlisted host spill paths.
const hostDataRootID = "host-data"

// spillReference places a relative wire spill reference in host data. An entry
// of that name under the current directory keeps its ordinary meaning.
func (p *ProgramPaths) spillReference(operand string) (string, bool) {
	host := strings.TrimSpace(p.context.Host.HostDataDir)
	if host == "" || filepath.IsAbs(operand) {
		return "", false
	}
	rel, ok := tooloutput.AgentWireSpillScopeRel(host, operand)
	if !ok {
		return "", false
	}
	if cwd := p.cwd.Resolved.Abs; cwd != "" {
		if _, err := os.Lstat(filepath.Join(cwd, operand)); !errors.Is(err, fs.ErrNotExist) {
			return "", false
		}
	}
	return tooloutput.DiskPath(host, rel), true
}

func spillExists(abs string) bool {
	_, err := os.Lstat(abs)
	return err == nil
}

// holdConvertedSpill refuses a converted spill no authorized read could open,
// and keeps a live one past reclamation's grace for the read that follows.
func holdConvertedSpill(host string, bound BoundPath) error {
	if _, err := bound.Stat(true); err != nil {
		return utilityPathError(bound.Resolved.DisplayPath, err)
	}
	// Reference counts still govern retention; a store that refuses the
	// timestamp leaves the converted path as valid as it was.
	_ = tooloutput.RenewSpillGrace(host, bound.Resolved.ScopeRel, time.Now())
	return nil
}
