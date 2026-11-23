package gitexec

import (
	"os"
	"runtime"
	"strings"

	"github.com/lycaon/lycaon/internal/gitengine"
)

func resolveLFSPath() (string, error) {
	path, err := gitengine.LFSPath()
	if err != nil {
		return "", err
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st.IsDir() || (runtime.GOOS != "windows" && st.Mode()&0o111 == 0) {
		return "", os.ErrNotExist
	}
	return path, nil
}

// lfsFilterArgs returns transient filter settings when the helper is available.
func (e execution) lfsFilterArgs() []string {
	path, err := e.lfsPath()
	if err != nil || path == "" {
		return nil
	}
	// Filter commands require a quoted path; %f remains a separate placeholder.
	q := shellQuote(path)
	return []string{
		"-c", "filter.lfs.clean=" + q + " clean -- %f",
		"-c", "filter.lfs.smudge=" + q + " smudge -- %f",
		"-c", "filter.lfs.process=" + q + " filter-process",
		"-c", "filter.lfs.required=true",
	}
}

// shellQuote renders s as a single POSIX shell word.
func shellQuote(s string) string {
	return shellQuoteForOS(s, runtime.GOOS)
}

func shellQuoteForOS(s, goos string) string {
	if goos == "windows" {
		s = strings.ReplaceAll(s, `\`, "/")
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
