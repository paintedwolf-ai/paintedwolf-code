package git

import (
	"bytes"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"
)

type porcelainEntry struct {
	status string
	path   string
}

type porcelainParser struct {
	branch       string
	entries      []porcelainEntry
	renameSource bool
}

func (p *porcelainParser) record(record []byte) error {
	if p.renameSource {
		if _, err := parseRepoPath(record); err != nil {
			return err
		}
		p.renameSource = false
		return nil
	}
	if len(record) == 0 {
		return nil
	}
	if bytes.HasPrefix(record, []byte("## ")) {
		p.branch = string(record[3:])
		return nil
	}
	if len(record) < 4 || record[2] != ' ' {
		return fmt.Errorf("parse git status: malformed porcelain record")
	}
	path, err := parseRepoPath(record[3:])
	if err != nil {
		return err
	}
	status := string(record[:2])
	p.entries = append(p.entries, porcelainEntry{status: status, path: path})
	p.renameSource = status[0] == 'R' || status[0] == 'C' || status[1] == 'R' || status[1] == 'C'
	return nil
}

func (p *porcelainParser) finish() (string, []porcelainEntry, error) {
	if p.renameSource {
		return "", nil, fmt.Errorf("parse git status: rename source missing")
	}
	return p.branch, p.entries, nil
}

type numstatParser struct {
	stats       []GitDiffStatEntry
	pending     GitDiffStatEntry
	renameStage int
}

func (p *numstatParser) record(record []byte) error {
	if p.renameStage != 0 {
		path, err := parseRepoPath(record)
		if err != nil {
			return err
		}
		if p.renameStage == 1 {
			p.renameStage = 2
			return nil
		}
		p.pending.Path = path
		p.stats = append(p.stats, p.pending)
		p.renameStage = 0
		return nil
	}
	if len(record) == 0 {
		return nil
	}
	fields := bytes.SplitN(record, []byte{'\t'}, 3)
	if len(fields) != 3 {
		return fmt.Errorf("parse git numstat: malformed record")
	}
	p.pending = GitDiffStatEntry{Insertions: parseNumstatCount(fields[0]), Deletions: parseNumstatCount(fields[1])}
	if len(fields[2]) == 0 {
		p.renameStage = 1
		return nil
	}
	path, err := parseRepoPath(fields[2])
	if err != nil {
		return err
	}
	p.pending.Path = path
	p.stats = append(p.stats, p.pending)
	return nil
}

func (p *numstatParser) finish() ([]GitDiffStatEntry, error) {
	if p.renameStage != 0 {
		return nil, fmt.Errorf("parse git numstat: rename paths missing")
	}
	return p.stats, nil
}

func parseNumstatCount(raw []byte) int {
	if bytes.Equal(raw, []byte{'-'}) {
		return 0
	}
	n, _ := strconv.Atoi(string(raw))
	return n
}

type lastTouchParser struct {
	result    map[string]time.Time
	current   time.Time
	firstPath bool
}

func (p *lastTouchParser) record(record []byte) error {
	if len(record) == 0 {
		return nil
	}
	if record[0] == 0x1e {
		sec, err := strconv.ParseInt(string(record[1:]), 10, 64)
		if err != nil {
			return fmt.Errorf("parse git history timestamp: %w", err)
		}
		p.current = time.Unix(sec, 0).UTC()
		p.firstPath = true
		return nil
	}
	if p.firstPath && record[0] == '\n' {
		record = record[1:]
	}
	p.firstPath = false
	path, err := parseRepoPath(record)
	if err != nil {
		return err
	}
	if p.result == nil {
		p.result = make(map[string]time.Time)
	}
	if _, seen := p.result[path]; !seen && !p.current.IsZero() {
		p.result[path] = p.current
	}
	return nil
}

func parseRepoPath(raw []byte) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("parse git path: empty path")
	}
	if !utf8.Valid(raw) {
		return "", fmt.Errorf("parse git path: non-UTF-8 path is unsupported")
	}
	return string(raw), nil
}
