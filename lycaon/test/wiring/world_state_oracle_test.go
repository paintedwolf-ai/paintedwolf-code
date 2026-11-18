package wiring

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testutil"
)

// worldState is what a refused operation must leave as it found. It is read
// with the standard library, git and SQL only, and shares no code with the gate.
type worldState struct {
	project, outside string
	git              string
	rows             string
	grants           string
}

// worldProbe names what one session can change.
type worldProbe struct {
	db               db.ReadHandle
	session          string
	project, outside string
	grantFile        string
}

func (p worldProbe) read(t *testing.T) worldState {
	t.Helper()
	return worldState{
		project: treeDigest(t, p.project),
		outside: treeDigest(t, p.outside),
		git:     gitDigest(t, p.project),
		rows:    p.rowDigest(t),
		grants:  p.grantDigest(t),
	}
}

// diff names each part of the world that changed.
func (w worldState) diff(after worldState) []string {
	var changed []string
	for _, part := range []struct{ name, before, after string }{
		{"project tree", w.project, after.project},
		{"outside tree", w.outside, after.outside},
		{"git state", w.git, after.git},
		{"source and effect rows", w.rows, after.rows},
		{"grants", w.grants, after.grants},
	} {
		if part.before != part.after {
			changed = append(changed, part.name)
		}
	}
	return changed
}

// treeDigest hashes every entry's path, type, mode, owner, link target and
// bytes. Git's object store is covered by gitDigest instead.
func treeDigest(t *testing.T, root string) string {
	t.Helper()
	if root == "" {
		return ""
	}
	sum := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if entry.IsDir() && rel == ".git" {
			return filepath.SkipDir
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		owner := ""
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			owner = fmt.Sprintf("%d:%d", stat.Uid, stat.Gid)
		}
		fmt.Fprintf(sum, "%s\x00%s\x00%s\x00", rel, info.Mode(), owner)
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(sum, "%s\x00", target)
		case info.Mode().IsRegular():
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(sum, f)
			_ = f.Close()
			if err != nil {
				return err
			}
		}
		return nil
	})
	testutil.FailErr(t, "hash tree "+root, err)
	return hex.EncodeToString(sum.Sum(nil))
}

// gitDigest records HEAD, every ref, the index bytes and porcelain status.
func gitDigest(t *testing.T, dir string) string {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return ""
	}
	var parts []string
	for _, args := range [][]string{
		{"rev-parse", "HEAD"},
		{"for-each-ref", "--format=%(refname) %(objectname)"},
		{"stash", "list"},
		{"status", "--porcelain=v2", "--untracked-files=all"},
	} {
		out, err := worldGit(dir, args...)
		testutil.FailErr(t, "git "+strings.Join(args, " "), err)
		parts = append(parts, out)
	}
	index, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	testutil.FailErr(t, "read git index", err)
	parts = append(parts, hex.EncodeToString(index))
	return strings.Join(parts, "\x00")
}

func worldGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, out)
	}
	return string(out), nil
}

// rowDigest counts the session's source changes and effect records.
func (p worldProbe) rowDigest(t *testing.T) string {
	t.Helper()
	var parts []string
	for _, query := range []string{
		`SELECT COUNT(*) FROM source_operations WHERE session_id = ?`,
		`SELECT COUNT(*) FROM source_effects e JOIN source_operations o ON o.id = e.operation_id WHERE o.session_id = ?`,
		`SELECT COUNT(*) FROM chat_grants WHERE chat_session_id = ?`,
	} {
		var n int
		testutil.FailErr(t, "count rows", p.db.QueryRowContext(t.Context(), query, p.session).Scan(&n))
		parts = append(parts, fmt.Sprint(n))
	}
	return strings.Join(parts, ",")
}

// grantDigest reads the durable grant file and the session grant rows.
func (p worldProbe) grantDigest(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(p.grantFile)
	if err != nil && !os.IsNotExist(err) {
		testutil.FailErr(t, "read grant file", err)
	}
	rows, err := p.db.QueryContext(t.Context(), `SELECT id, delta_json FROM chat_grants WHERE chat_session_id = ? ORDER BY id`, p.session)
	testutil.FailErr(t, "read session grants", err)
	defer rows.Close()
	var grants []string
	for rows.Next() {
		var id, delta string
		testutil.FailErr(t, "scan grant", rows.Scan(&id, &delta))
		grants = append(grants, id+"="+delta)
	}
	testutil.FailErr(t, "iterate grants", rows.Err())
	sort.Strings(grants)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]) + "|" + strings.Join(grants, ";")
}
