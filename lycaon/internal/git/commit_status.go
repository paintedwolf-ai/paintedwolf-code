package git

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/gitexec"
)

// CommitStatus holds Git's current index/worktree facts against one HEAD.
type CommitStatus struct {
	Head  string
	Files []CommitStatusFile
}

type CommitStatusFile struct {
	Path, FromPath, Status, Submodule string
}

func (m *Manager) CommitStatus(ctx context.Context, projectDir string) (CommitStatus, error) {
	dir, err := repositoryProjectDir(projectDir)
	if err != nil {
		return CommitStatus{}, err
	}
	var p commitStatusParser
	err = gitexec.RunRecords(ctx, dir, []string{"status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all", "--ignore-submodules=none"}, hermeticOpts(0), p.record)
	if err != nil {
		return CommitStatus{}, err
	}
	return p.finish()
}

type commitStatusParser struct {
	out          CommitStatus
	seenHead     bool
	renameSource bool
}

func (p *commitStatusParser) record(raw []byte) error {
	if p.renameSource {
		path, err := parseRepoPath(raw)
		if err != nil {
			return err
		}
		p.out.Files[len(p.out.Files)-1].FromPath = path
		p.renameSource = false
		return nil
	}
	record := string(raw)
	if record == "" {
		return nil
	}
	if head, ok := strings.CutPrefix(record, "# branch.oid "); ok {
		p.seenHead = true
		if head != "(initial)" {
			p.out.Head = head
		}
		return nil
	}
	if strings.HasPrefix(record, "# ") {
		return nil
	}
	file, rename, err := parseCommitStatusFile(record)
	if err != nil {
		return err
	}
	p.out.Files = append(p.out.Files, file)
	p.renameSource = rename
	return nil
}

func (p *commitStatusParser) finish() (CommitStatus, error) {
	if p.renameSource {
		return CommitStatus{}, fmt.Errorf("git status rename source missing")
	}
	if !p.seenHead {
		return CommitStatus{}, fmt.Errorf("git status did not report HEAD")
	}
	return p.out, nil
}

func parseCommitStatusFile(record string) (CommitStatusFile, bool, error) {
	var file CommitStatusFile
	if strings.HasPrefix(record, "? ") {
		path, err := parseRepoPath([]byte(record[2:]))
		return CommitStatusFile{Path: path, Status: "??"}, false, err
	}
	n := 9
	switch record[0] {
	case '1':
	case '2':
		n = 10
	case 'u':
		n = 11
	default:
		return file, false, fmt.Errorf("unknown git status record")
	}
	fields := strings.SplitN(record, " ", n)
	if len(fields) != n || len(fields[1]) != 2 {
		return file, false, fmt.Errorf("malformed git status record")
	}
	path, err := parseRepoPath([]byte(fields[n-1]))
	if err != nil {
		return file, false, err
	}
	file = CommitStatusFile{Path: path, Status: fields[1], Submodule: fields[2]}
	return file, record[0] == '2', nil
}
