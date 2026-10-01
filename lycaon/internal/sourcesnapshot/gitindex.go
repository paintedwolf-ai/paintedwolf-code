package sourcesnapshot

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/gitrepo"
)

// gitIndex holds every regular tracked file whose worktree bytes match the
// index, with the blob id git computed for it. A changed file is absent and
// identified by reading it. Ids are raw so a large index costs the paths
// plus twenty bytes each.
type gitIndex struct {
	clean map[string][20]byte
}

// lookup returns the blob id of a file clean against the index.
func (g *gitIndex) lookup(rel string) (string, bool) {
	if g == nil {
		return "", false
	}
	oid, ok := g.clean[rel]
	if !ok {
		return "", false
	}
	return hex.EncodeToString(oid[:]), true
}

var (
	indexModeFile       = []byte("100644")
	indexModeExecutable = []byte("100755")
	indexStageMerged    = []byte("0")
)

// readGitIndex lists the index under root and removes what the worktree
// changed. A root outside any repository yields nil; git failing to answer
// is an error the caller treats as no index.
func readGitIndex(ctx context.Context, root string) (*gitIndex, error) {
	repo, ok := gitrepo.Discover(root)
	if !ok {
		return nil, nil
	}
	prefix, err := filepath.Rel(repo.Root, root)
	if err != nil {
		return nil, err
	}
	prefix = filepath.ToSlash(prefix)
	if prefix == "." {
		prefix = ""
	} else {
		prefix += "/"
	}
	opts := gitexec.Opts{Profile: gitexec.ProfileHermetic, MaxOutput: exec.DefaultMaxScanOutputBytes}
	index := &gitIndex{clean: make(map[string][20]byte, 1024)}
	conflicted := make(map[string]struct{})
	err = gitexec.RunRecords(ctx, root, []string{"ls-files", "--stage", "-z"}, opts, func(record []byte) error {
		if len(record) == 0 {
			return nil
		}
		mode, oid, stage, rel, err := parseIndexRecord(record)
		if err != nil {
			return err
		}
		if !bytes.Equal(stage, indexStageMerged) {
			path := string(rel)
			conflicted[path] = struct{}{}
			delete(index.clean, path)
			return nil
		}
		if !bytes.Equal(mode, indexModeFile) && !bytes.Equal(mode, indexModeExecutable) {
			return nil
		}
		var raw [20]byte
		if _, err := hex.Decode(raw[:], oid); err != nil {
			return fmt.Errorf("git ls-files: malformed object id")
		}
		path := string(rel)
		if _, ok := conflicted[path]; ok {
			return nil
		}
		index.clean[path] = raw
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	var status statusParser
	err = gitexec.RunRecords(ctx, root, []string{
		"--no-optional-locks", "status", "--porcelain=v2", "-z", "--untracked-files=no", "--ignore-submodules=all", "--", ".",
	}, opts, status.record)
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}
	for _, rel := range status.dirty {
		delete(index.clean, strings.TrimPrefix(rel, prefix))
	}
	return index, nil
}

// parseIndexRecord splits "<mode> <oid> <stage>\t<path>" without copying.
func parseIndexRecord(record []byte) (mode, oid, stage, rel []byte, err error) {
	tab := bytes.IndexByte(record, '\t')
	if tab < 0 {
		return nil, nil, nil, nil, fmt.Errorf("git ls-files: malformed record")
	}
	fields := bytes.Fields(record[:tab])
	if len(fields) != 3 || len(fields[1]) != 40 {
		return nil, nil, nil, nil, fmt.Errorf("git ls-files: malformed record")
	}
	return fields[0], fields[1], fields[2], record[tab+1:], nil
}

// statusParser collects the paths whose worktree bytes differ from the index
// from porcelain v2 records. Paths are repository-relative.
type statusParser struct {
	dirty        []string
	renameSource bool
}

func (p *statusParser) record(record []byte) error {
	if p.renameSource {
		// The record after a rename is its source path; the index already
		// holds the destination, which the rename record named.
		p.renameSource = false
		return nil
	}
	if len(record) < 2 {
		return nil
	}
	text := string(record)
	switch text[0] {
	case '1':
		fields := strings.SplitN(text, " ", 9)
		if len(fields) != 9 {
			return fmt.Errorf("git status: malformed record")
		}
		if worktreeDiffers(fields[1]) {
			p.dirty = append(p.dirty, fields[8])
		}
	case '2':
		fields := strings.SplitN(text, " ", 10)
		if len(fields) != 10 {
			return fmt.Errorf("git status: malformed record")
		}
		if worktreeDiffers(fields[1]) {
			p.dirty = append(p.dirty, fields[9])
		}
		p.renameSource = true
	case 'u':
		fields := strings.SplitN(text, " ", 11)
		if len(fields) != 11 {
			return fmt.Errorf("git status: malformed record")
		}
		p.dirty = append(p.dirty, fields[10])
	}
	return nil
}

// worktreeDiffers reads the Y column of an XY status: the worktree against
// the index. A staged change with a clean worktree is clean against the
// index, which is the comparison that matters here.
func worktreeDiffers(xy string) bool {
	return len(xy) != 2 || xy[1] != '.'
}
