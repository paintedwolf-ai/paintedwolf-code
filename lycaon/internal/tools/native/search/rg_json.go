package search

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/textsearch"
)

// rgJSON is rg --json: ripgrep's JSON Lines messages. Each file with output
// gets begin, match/context and end messages; one summary closes the run.
// Field order, escaping and statistics follow rg 15; elapsed times are this
// search's own measurements.
type rgJSON struct {
	started time.Time
	total   rgStats
}

type rgStats struct {
	elapsed                                time.Duration
	searches, withMatch, searched, printed int64
	matchedLines, matches                  int64
}

// jsonContent searches one input and writes its messages.
func (r *grepRun) jsonContent(label string, content []byte) error {
	inv := r.inv
	started := time.Now()
	if memory := inv.owners.Revisions.Memory; memory != nil {
		release, err := memory.Reserve(inv.ctx, int64(len(label))*20+256)
		if err != nil {
			return err
		}
		defer release()
	}
	var file rgStats
	file.searches = 1
	path := jsonData(label)
	begun := false
	emit := func(line string) error {
		file.printed += int64(len(line)) + 1
		return inv.out.text(line, "\n")
	}
	lastSelectedEnd := 0
	binaryOffset := bytes.IndexByte(content, 0)
	n, err := textsearch.ScanReader(inv.ctx, bytes.NewReader(content), 0, nil, r.matcher, r.scan, func(line textsearch.Line) error {
		if !begun {
			begun = true
			if err := emit(`{"type":"begin","data":{"path":` + path + `}}`); err != nil {
				return err
			}
		}
		end := line.End
		if end < len(content) && content[end] == '\n' {
			end++
		}
		record := content[line.Start:line.End]
		kind := "context"
		if line.Kind == textsearch.SelectedLine {
			kind = "match"
			lastSelectedEnd = end
			file.matchedLines++
		}
		output := &grepRecord{inv: inv}
		defer output.close()
		if err := output.text(`{"type":"`, kind, `","data":{"path":`, path, `,"lines":`); err != nil {
			return err
		}
		if err := output.jsonData(content[line.Start:end]); err != nil {
			return err
		}
		if err := output.text(`,"line_number":`, strconv.Itoa(line.Number), `,"absolute_offset":`, strconv.Itoa(line.Start), `,"submatches":[`); err != nil {
			return err
		}
		if line.Kind == textsearch.SelectedLine && !r.scan.Invert {
			count, err := r.jsonSubmatches(output, record)
			if err != nil {
				return err
			}
			file.matches += count
		}
		if err := output.text(`]}}`, "\n"); err != nil {
			return err
		}
		file.printed += int64(len(output.bytes))
		_, err := inv.out.Write(output.bytes)
		return err
	})
	if err != nil {
		return err
	}
	file.searched = int64(len(content))
	if r.scan.MaxSelected > 0 && n >= r.scan.MaxSelected {
		file.searched = int64(lastSelectedEnd)
	}
	if binaryOffset >= 0 {
		// JSON preserves complete records, including NUL bytes. ripgrep's
		// searched-byte statistic ends at the first detected binary byte.
		file.searched = min(file.searched, int64(binaryOffset))
	}
	if n > 0 {
		r.selected = true
		file.withMatch = 1
	}
	file.elapsed = time.Since(started)
	r.json.total.add(file)
	if !begun {
		return nil
	}
	binary := "null"
	if binaryOffset >= 0 {
		binary = strconv.Itoa(binaryOffset)
	}
	return inv.out.text(`{"type":"end","data":{"path":`+path+`,"binary_offset":`+binary+`,"stats":{"elapsed":`+elapsedOrdered(file.elapsed)+
		`,"searches":`+itoa(file.searches)+`,"searches_with_match":`+itoa(file.withMatch)+`,"bytes_searched":`+itoa(file.searched)+
		`,"bytes_printed":`+itoa(file.printed)+`,"matched_lines":`+itoa(file.matchedLines)+`,"matches":`+itoa(file.matches)+`}}}`, "\n")
}

// jsonSubmatches lists a selected record's matches, with replacements under -r.
func (r *grepRun) jsonSubmatches(output *grepRecord, record []byte) (int64, error) {
	var count int64
	err := r.matcher.Groups(r.inv.ctx, record, func(groups []int) error {
		if count > 0 {
			if err := output.text(","); err != nil {
				return err
			}
		}
		if err := output.text(`{"match":`); err != nil {
			return err
		}
		if err := output.jsonData(record[groups[0]:groups[1]]); err != nil {
			return err
		}
		if r.replace != nil {
			replacement := &grepRecord{inv: r.inv}
			defer replacement.close()
			if err := replacement.replacement(*r.replace, record, groups); err != nil {
				return err
			}
			if err := output.text(`,"replacement":`); err != nil {
				return err
			}
			if err := output.jsonData(replacement.bytes); err != nil {
				return err
			}
		}
		count++
		return output.text(`,"start":`, strconv.Itoa(groups[0]), `,"end":`, strconv.Itoa(groups[1]), "}")
	})
	return count, err
}

// summary closes a --json run; rg writes it with its keys sorted.
func (j *rgJSON) summary() string {
	t := j.total
	return `{"data":{"elapsed_total":` + elapsedSorted(time.Since(j.started)) + `,"stats":{"bytes_printed":` + itoa(t.printed) +
		`,"bytes_searched":` + itoa(t.searched) + `,"elapsed":` + elapsedSorted(t.elapsed) + `,"matched_lines":` + itoa(t.matchedLines) +
		`,"matches":` + itoa(t.matches) + `,"searches":` + itoa(t.searches) + `,"searches_with_match":` + itoa(t.withMatch) + `}},"type":"summary"}` + "\n"
}

func (s *rgStats) add(o rgStats) {
	s.elapsed += o.elapsed
	s.searches += o.searches
	s.withMatch += o.withMatch
	s.searched += o.searched
	s.printed += o.printed
	s.matchedLines += o.matchedLines
	s.matches += o.matches
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func elapsedOrdered(d time.Duration) string {
	return fmt.Sprintf(`{"secs":%d,"nanos":%d,"human":"%.6fs"}`, int64(d/time.Second), int64(d%time.Second), d.Seconds())
}

func elapsedSorted(d time.Duration) string {
	return fmt.Sprintf(`{"human":"%.6fs","nanos":%d,"secs":%d}`, d.Seconds(), int64(d%time.Second), int64(d/time.Second))
}

// jsonData is rg's arbitrary data: {"text":...} for UTF-8, otherwise
// {"bytes":BASE64}.
func jsonData(s string) string {
	if !utf8.ValidString(s) {
		return `{"bytes":"` + base64.StdEncoding.EncodeToString([]byte(s)) + `"}`
	}
	return `{"text":` + jsonString(s) + `}`
}

// jsonString escapes as serde_json does: quote, backslash and control
// characters only, with \u00xx in lowercase hex for controls without a
// short escape.
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if c < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
