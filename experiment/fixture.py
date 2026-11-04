"""Fixture and tasks for the numbered source experiment. Fixed before any measured run."""

REPORT_PY = '''"""Report layout."""

COLUMNS = {
    "name":    12,
    "region":   8,
    "total":   10,
}

BANNER = """
Totals:\x20\x20
========
"""


def header(width):
    line = "-" * width
    return line + "\\n" + BANNER


def row(name, region, total):
    return (
        f"{name:<12}"
        f"{region:>8}"
        f"{total:>10}"
    )
'''

QUEUE_GO = '''package queue

import "fmt"

const (
	retryMax  = 3
	batchSize = 16
)

// Queue holds pending items.
type Queue struct {
	items []string
	limit int
}

// Drain prints items up to the limit.
func (q *Queue) Drain() int {
	n := 0
	for _, item := range q.items {
		if item != "" {
			if n > q.limit {
				break
			}
			fmt.Printf("%s\\t%d\\n", item, n)
			n++
		}
	}
	return n
}
'''

STORE_GO = '''package store

import (
	"fmt"
	"log"
	"os"
)

// File is an opened data file.
type File struct {
	f *os.File
}

// Open opens path for reading.
func Open(path string) (*File, error) {
	log.Printf("opening %s", path)
	f, err := os.Open(path)
	if err != nil {
		log.Printf("open %s: %v", path, err)
		log.Printf("giving up on %s", path)
		return nil, err
	}
	return &File{f: f}, nil
}

// Name reports the file name.
func (s *File) Name() string {
	return fmt.Sprintf("store:%s", s.f.Name())
}
'''

WINDOWS_YAML = '''# Maintenance windows keyed by hour.
# Each key is an hour of the day.
schedule:
     3: nightly-backup
     5: compact-index
     6: refresh-stats
     7: rotate-logs
    12: cache-warm
    18: export-usage
'''

RETRY_GO = '''package retry

import "time"

// Fast returns the deadline for quick calls.
func Fast() time.Duration {
	timeout := 30 * time.Second
	return timeout / 2
}

// Slow returns the deadline for batch calls.
func Slow() time.Duration {
	timeout := 30 * time.Second
	return timeout * 2
}
'''


def invoice_go():
    head = '''package billing

import (
	"errors"
	"math"
)

// taxRate is the flat sales tax applied to every invoice.
const taxRate = 0.0825

// lateFeeCents is added once to an overdue invoice.
const lateFeeCents = 1500

// ErrNegativeTotal reports an invoice that computes below zero.
var ErrNegativeTotal = errors.New("negative total")

// Line is one invoice row.
type Line struct {
	Cents    int64
	Quantity int64
}

// Invoice is a set of rows with its discount and due state.
type Invoice struct {
	Lines        []Line
	DiscountRate float64
	Overdue      bool
}
'''
    pad = ''
    for name in ('Count', 'Weight', 'Largest', 'Smallest'):
        pad += f'''
// {name} is a reporting helper for {name.lower()} figures.
func (inv Invoice) {name}() int64 {{
	var out int64
	for _, line := range inv.Lines {{
		if line.Quantity > 0 {{
			out += line.Quantity
		}}
	}}
	return out
}}
'''
    total = '''
// Subtotal adds every row before discount and tax.
func (inv Invoice) Subtotal() int64 {
	var sum int64
	for _, line := range inv.Lines {
		sum += line.Cents * line.Quantity
	}
	return sum
}

// Total computes the amount due in cents.
func (inv Invoice) Total() (int64, error) {
	amount := float64(inv.Subtotal())
	amount -= amount * inv.DiscountRate
	amount += amount * taxRate
	cents := int64(math.Round(amount))
	if inv.Overdue {
		cents += lateFeeCents
	}
	if cents < 0 {
		return 0, ErrNegativeTotal
	}
	return cents, nil
}
'''
    return head + pad + total


def parser_py():
    head = '''"""Parse the simple key file format."""

import re

COMMENT = "#"
MAX_LINE_BYTES = 4096
KEY_PATTERN = re.compile(r"^[a-z_]+$")


class ParseError(ValueError):
    """A line could not be parsed."""


'''
    pad = ''
    for name in ('count_blank', 'count_comment', 'count_long', 'count_short'):
        pad += f'''def {name}(lines):
    """Reporting helper for {name}."""
    total = 0
    for line in lines:
        if line:
            total += 1
    return total


'''
    body = '''def parse_line(raw):
    """Return (key, value), or None for a comment or blank line."""
    if len(raw) > MAX_LINE_BYTES:
        raise ParseError("line too long")
    text = raw.strip()
    if not text or text.startswith(COMMENT):
        return None
    key, sep, value = text.partition("=")
    if not sep:
        raise ParseError("missing separator")
    key = key.strip()
    if not KEY_PATTERN.match(key):
        raise ParseError("bad key")
    return key, value.strip()


def parse(lines):
    """Parse every line into a dictionary; the last duplicate wins."""
    out = {}
    for raw in lines:
        pair = parse_line(raw)
        if pair is None:
            continue
        key, value = pair
        out[key] = value
    return out
'''
    return head + pad + body


def files():
    return {
        'README.md': '# ledger\n\nSmall sample services.\n',
        'go.mod': 'module example.test/ledger\n\ngo 1.22\n',
        'report/report.py': REPORT_PY,
        'internal/queue/queue.go': QUEUE_GO,
        'internal/store/store.go': STORE_GO,
        'deploy/windows.yaml': WINDOWS_YAML,
        'internal/retry/retry.go': RETRY_GO,
        'internal/billing/invoice.go': invoice_go(),
        'tools/parser.py': parser_py(),
    }


def _sub(text, old, new):
    assert text.count(old) == 1, old
    return text.replace(old, new)


READ = 'First inspect the file with the summarize tool, not with command. In a later response, make the change yourself with the edit tool; do not start a worker. '
CITE = 'Inspect the file with the summarize tool, not with command. Do not change anything. '
FORMAT = 'Answer with one bullet per item, starting with its letter, a sentence of explanation, and a citation written as path:line in backticks, for the single line that does it.'

TASKS = {
    'whitespace': dict(kind='edit', path='report/report.py',
        prompt=READ + 'In report/report.py: rename the banner heading from Totals: to Subtotals:, keeping the two trailing spaces that line has today. Also change the region column value from 8 to 14, leaving the spacing after the colon as it is. Change nothing else.',
        expected=lambda t: _sub(_sub(t, 'Totals:  \n', 'Subtotals:  \n'), '"region":   8,', '"region":   14,')),
    'tabs': dict(kind='edit', path='internal/queue/queue.go',
        prompt=READ + 'In internal/queue/queue.go: change the limit check in Drain from n > q.limit to n >= q.limit, and change retryMax from 3 to 5, keeping its existing alignment. Change nothing else.',
        expected=lambda t: _sub(_sub(t, 'if n > q.limit {', 'if n >= q.limit {'), 'retryMax  = 3', 'retryMax  = 5')),
    'multiline': dict(kind='edit', path='internal/store/store.go',
        prompt=READ + 'In internal/store/store.go, Open: replace the three lines inside the err check (both log.Printf lines and the return) with this single line, indented to match: return nil, fmt.Errorf("open %s: %w", path, err)\nUse one edit call whose old_string spans those lines. Change nothing else.',
        expected=lambda t: _sub(t, '\t\tlog.Printf("open %s: %v", path, err)\n\t\tlog.Printf("giving up on %s", path)\n\t\treturn nil, err\n', '\t\treturn nil, fmt.Errorf("open %s: %w", path, err)\n')),
    'colon_digits': dict(kind='edit', path='deploy/windows.yaml',
        prompt=READ + 'In deploy/windows.yaml: delete the whole entry for hour 3, and change the value of the hour 12 entry from cache-warm to cache-warm-full. The file\'s keys are right-aligned numbers; keep every remaining line\'s leading spaces and key exactly as they are. Change nothing else.',
        expected=lambda t: _sub(_sub(t, '     3: nightly-backup\n', ''), '    12: cache-warm\n', '    12: cache-warm-full\n')),
    'duplicate': dict(kind='edit', path='internal/retry/retry.go',
        prompt=READ + 'In internal/retry/retry.go: change the timeout in Slow from 30 to 90 seconds. Fast must stay at 30. Change nothing else.',
        expected=lambda t: _sub(t, 'func Slow() time.Duration {\n\ttimeout := 30 * time.Second', 'func Slow() time.Duration {\n\ttimeout := 90 * time.Second')),
    'cite_invoice': dict(kind='cite', path='internal/billing/invoice.go',
        prompt=CITE + 'Explain how Total in internal/billing/invoice.go computes the amount due. Items: (a) where the discount is applied, (b) where tax is added, (c) where the amount is rounded, (d) where the late fee is added, (e) where a negative total is rejected. ' + FORMAT,
        truth={'a': 'amount -= amount * inv.DiscountRate', 'b': 'amount += amount * taxRate', 'c': 'cents := int64(math.Round(amount))', 'd': 'cents += lateFeeCents', 'e': 'return 0, ErrNegativeTotal'}),
    'cite_parser': dict(kind='cite', path='tools/parser.py',
        prompt=CITE + 'Explain how parse_line in tools/parser.py handles a line. Items: (a) where an overlong line is refused, (b) where comment and blank lines are skipped, (c) where the key and value are split, (d) where a missing separator is refused, (e) where a bad key is refused. ' + FORMAT,
        truth={'a': 'raise ParseError("line too long")', 'b': 'if not text or text.startswith(COMMENT):', 'c': 'key, sep, value = text.partition("=")', 'd': 'raise ParseError("missing separator")', 'e': 'raise ParseError("bad key")'}),
}
CLASSES = {'whitespace': 'whitespace', 'tabs': 'tabs', 'multiline': 'multiline', 'colon_digits': 'colon_digits', 'duplicate': 'duplicate', 'cite_invoice': 'citation', 'cite_parser': 'citation'}
