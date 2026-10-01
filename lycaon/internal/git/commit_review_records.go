package git

import (
	"errors"
	"strconv"
	"strings"
)

type commitReviewParser struct {
	opts       CommitReviewOptions
	bounded    bool
	bytes      int
	page       CommitReviewPage
	selected   map[string]int
	pending    *CommitReviewFile
	renamePath bool
	statPaths  int
	statCount  int
	statRow    CommitReviewFile
}

func newCommitReviewParser(opts CommitReviewOptions) *commitReviewParser {
	return &commitReviewParser{opts: opts, page: CommitReviewPage{Files: []CommitReviewFile{}}, selected: map[string]int{}}
}

func (p *commitReviewParser) raw(raw []byte) error {
	value := string(raw)
	if p.pending == nil {
		fields := strings.Fields(value)
		if len(fields) != 5 || !strings.HasPrefix(fields[0], ":") || len(fields[4]) == 0 {
			return errors.New("invalid commit raw metadata")
		}
		p.pending = &CommitReviewFile{BeforeMode: fields[0][1:], AfterMode: fields[1], BeforeOID: nonzeroOID(fields[2]), AfterOID: nonzeroOID(fields[3]), Status: fields[4][:1]}
		p.renamePath = p.pending.Status == "R" || p.pending.Status == "C"
		return nil
	}
	if p.renamePath {
		p.pending.BeforePath = value
		p.renamePath = false
		return nil
	}
	row := *p.pending
	row.Path = value
	if row.BeforePath == "" {
		row.BeforePath = value
	}
	index := p.page.Total
	p.page.Total++
	if (p.opts.Path != "" && row.Path == p.opts.Path) || (p.opts.Path == "" && index >= p.opts.Offset && len(p.page.Files) < p.opts.Limit) {
		p.selected[row.Path] = len(p.page.Files)
		p.bytes += reviewFileBytes(row)
		if p.bounded && p.bytes > immutableEntryBytes {
			return errReviewCacheLimit
		}
		p.page.Files = append(p.page.Files, row)
	}
	p.pending = nil
	return nil
}

func nonzeroOID(oid string) string {
	if strings.Trim(oid, "0") == "" {
		return ""
	}
	return oid
}

func (p *commitReviewParser) stat(raw []byte) error {
	if p.statPaths > 0 {
		p.statPaths--
		if p.statPaths == 0 {
			p.applyStat(string(raw))
		}
		return nil
	}
	fields := strings.SplitN(string(raw), "\t", 3)
	if len(fields) != 3 {
		return errors.New("invalid commit numstat record")
	}
	p.statRow = CommitReviewFile{Binary: fields[0] == "-" && fields[1] == "-"}
	if !p.statRow.Binary {
		insertions, err := strconv.Atoi(fields[0])
		if err != nil || insertions < 0 {
			return errors.New("invalid commit insertion count")
		}
		deletions, err := strconv.Atoi(fields[1])
		if err != nil || deletions < 0 {
			return errors.New("invalid commit deletion count")
		}
		p.statRow.Insertions, p.statRow.Deletions = insertions, deletions
	}
	if fields[2] == "" {
		p.statPaths = 2
	} else {
		p.applyStat(fields[2])
	}
	return nil
}

func (p *commitReviewParser) applyStat(path string) {
	p.statCount++
	p.page.Insertions += p.statRow.Insertions
	p.page.Deletions += p.statRow.Deletions
	if index, ok := p.selected[path]; ok {
		row := &p.page.Files[index]
		row.Insertions, row.Deletions, row.Binary = p.statRow.Insertions, p.statRow.Deletions, p.statRow.Binary
	}
}

var errReviewCacheLimit = errors.New("comparison exceeds Git memory cache budget")

type commitReviewSnapshot struct {
	page      CommitReviewPage
	oversized bool
}

func reviewFileBytes(row CommitReviewFile) int {
	return 256 + len(row.Path) + len(row.BeforePath) + len(row.Status) + len(row.BeforeOID) + len(row.AfterOID) + len(row.BeforeMode) + len(row.AfterMode)
}

func projectCommitReview(full CommitReviewPage, opts CommitReviewOptions) CommitReviewPage {
	page := CommitReviewPage{Files: []CommitReviewFile{}, Total: full.Total, Insertions: full.Insertions, Deletions: full.Deletions}
	if opts.Path != "" {
		for _, file := range full.Files {
			if file.Path == opts.Path {
				page.Files = append(page.Files, file)
				break
			}
		}
		return page
	}
	start := min(opts.Offset, len(full.Files))
	end := min(start+opts.Limit, len(full.Files))
	page.Files = append(page.Files, full.Files[start:end]...)
	if end < len(full.Files) {
		page.NextOffset = end
	}
	return page
}
