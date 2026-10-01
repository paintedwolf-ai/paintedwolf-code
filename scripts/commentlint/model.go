package main

import (
	"regexp"
	"strings"
	"time"
)

const maxSourceBytes = 8 << 20

var workMarkerPattern = regexp.MustCompile(`\b(TODO|FIXME|HACK|XXX)\b`)

type stringFlags []string

func (values *stringFlags) String() string { return strings.Join(*values, ",") }

func (values *stringFlags) Set(value string) error {
	*values = append(*values, value)
	return nil
}

type comment struct {
	Language string
	Kind     string
	StartRow int
	StartCol int
	Text     string
}

type finding struct {
	Rule     string `json:"rule"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Language string `json:"language"`
	Kind     string `json:"kind"`
	Message  string `json:"message"`
	Comment  string `json:"comment"`
}

type report struct {
	FilesVisited          int                       `json:"files_visited"`
	FilesScanned          int                       `json:"files_scanned"`
	FilesValidated        int                       `json:"files_validated"`
	FilesUnsupported      int                       `json:"files_unsupported"`
	FilesSkipped          int                       `json:"files_skipped"`
	ParseTimeouts         []string                  `json:"parse_timeouts"`
	Comments              int                       `json:"comments"`
	Findings              []finding                 `json:"findings"`
	Languages             map[string]languageReport `json:"languages"`
	UnsupportedExtensions map[string]int            `json:"unsupported_extensions"`
	ElapsedMilliseconds   int64                     `json:"elapsed_milliseconds"`
}

type languageReport struct {
	Files     int `json:"files"`
	Scanned   int `json:"scanned"`
	Validated int `json:"validated"`
	Timeouts  int `json:"timeouts"`
	Comments  int `json:"comments"`
}

type fileConfig struct {
	MaxLineLength           *int              `json:"max_line_length"`
	WorkMarkersRequireIssue *bool             `json:"work_markers_require_issue"`
	IssuePattern            string            `json:"issue_pattern"`
	LocalHomePaths          *bool             `json:"local_home_paths"`
	ParseTimeout            string            `json:"parse_timeout"`
	Excludes                []string          `json:"excludes"`
	Forbid                  map[string]string `json:"forbid"`
}

type namedPattern struct {
	name    string
	pattern *regexp.Regexp
}

type options struct {
	root                    string
	maxLineLength           int
	workMarkersRequireIssue bool
	issuePattern            *regexp.Regexp
	localHomePath           *regexp.Regexp
	forbidden               []namedPattern
	excludes                []string
	parseTimeout            time.Duration
	workers                 int
	reportOnly              bool
	json                    bool
}

type fileJob struct {
	path string
	rel  string
}

type fileResult struct {
	language             string
	scanned              bool
	validated            bool
	skipped              bool
	unsupportedExtension string
	timedOut             bool
	comments             int
	findings             []finding
	err                  error
}
