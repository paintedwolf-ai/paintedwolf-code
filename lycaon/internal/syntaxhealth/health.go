// Package syntaxhealth classifies source syntax and parser failures.
package syntaxhealth

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/filekind"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// Status distinguishes valid syntax from incomplete or failed analysis.
type Status string

const (
	StatusUnsupported Status = "unsupported"
	StatusOverridden  Status = "overridden"
	StatusClean       Status = "clean"
	StatusBroken      Status = "broken"
	StatusIncomplete  Status = "incomplete"
	StatusFailed      Status = "failed"
)

const (
	maxDiagnostics      = 20
	maxCollectedFaults  = 512
	syntaxSnippetMaxLen = 80
)

const (
	DiagnosticError   = "error"
	DiagnosticMissing = "missing"
)

// Diagnostic locates a recovered or missing syntax node.
type Diagnostic struct {
	Row       int    `json:"row"`
	Col       int    `json:"col"`
	EndRow    int    `json:"end_row"`
	EndCol    int    `json:"end_col"`
	StartByte int    `json:"start_byte"`
	EndByte   int    `json:"end_byte"`
	Snippet   string `json:"snippet"`
	Kind      string `json:"kind"`
	NodeType  string `json:"node_type,omitempty"`
}

// Burden is a monotonic syntax-repair score. SpanBytes measures the union of
// leaf recovery spans; Faults breaks ties and counts zero-width missing nodes.
type Burden struct {
	SpanBytes int `json:"span_bytes"`
	Faults    int `json:"faults"`
}

// Report is the complete parser-health result for one source snapshot.
type Report struct {
	Language             string           `json:"language,omitempty"`
	Status               Status           `json:"status"`
	Diagnostics          []Diagnostic     `json:"diagnostics,omitempty"`
	DiagnosticsTruncated bool             `json:"diagnostics_truncated,omitempty"`
	Burden               Burden           `json:"burden"`
	Failure              *tsparse.Failure `json:"failure,omitempty"`
}

// Analyze classifies one complete source snapshot under the selected host budget.
func Analyze(ctx context.Context, langName, filename string, src []byte, purpose tsparse.Purpose) (report Report) {
	if failure := tsparse.CancellationFailure(ctx, langName, len(src)); failure != nil {
		return Report{Language: failure.Language, Status: StatusIncomplete, Failure: failure}
	}
	entry := resolveEntry(ctx, langName, filename, src)
	if entry == nil || entry.Language == nil || !supported(entry.Name) {
		return Report{Status: StatusUnsupported}
	}
	report.Language = entry.Name
	defer func() {
		if recovered := recover(); recovered != nil {
			cause := fmt.Errorf("parser panic: %v", recovered)
			report = Report{
				Language: entry.Name,
				Status:   StatusFailed,
				Failure:  &tsparse.Failure{Reason: "panic", Language: entry.Name, SourceBytes: len(src), Detail: cause.Error(), Cause: cause},
			}
		}
	}()
	tree, err := tsparse.Parse(ctx, entry.Language(), src, purpose)
	if err != nil {
		report.Status = StatusFailed
		if errors.As(err, &report.Failure) && report.Failure.Incomplete() {
			report.Status = StatusIncomplete
		}
		return report
	}
	defer tree.Release()
	faults := collectLeafFaults(tree.RootNode())
	if report.Language == "python" {
		faults = append(faults, pythonStructuralFaults(tree.RootNode(), entry.Language(), src)...)
	}
	if len(faults) == 0 {
		report.Status = StatusClean
		return report
	}
	report.Status = StatusBroken
	report.Burden = faultBurden(faults)
	report.Diagnostics, report.DiagnosticsTruncated = buildDiagnostics(faults, src, entry.Language())
	return report
}

// Grammar recovery can hide unclosed strings and handlerless try statements.
func pythonStructuralFaults(root *gotreesitter.Node, language *gotreesitter.Language, src []byte) []fault {
	var out []fault
	gotreesitter.Walk(root, func(node *gotreesitter.Node, _ int) gotreesitter.WalkAction {
		if node == nil {
			return gotreesitter.WalkContinue
		}
		typ := node.Type(language)
		if typ == "string_start" && node.Parent() != nil && node.Parent().Type(language) != "string" {
			out = append(out, fault{node: node, start: int(node.StartByte()), end: int(node.EndByte()), kind: DiagnosticMissing})
			return gotreesitter.WalkContinue
		}
		if node.ChildCount() != 0 || typ != "try" || string(node.Text(src)) != "try" {
			return gotreesitter.WalkContinue
		}
		statement := node.Parent()
		for statement != nil && statement.Type(language) != "try_statement" {
			statement = statement.Parent()
		}
		if statement == nil || !pythonTryHasHandler(statement, language) {
			out = append(out, fault{
				node: node, start: int(node.StartByte()), end: int(node.EndByte()), kind: DiagnosticError,
			})
		}
		return gotreesitter.WalkContinue
	})
	return out
}

func pythonTryHasHandler(statement *gotreesitter.Node, language *gotreesitter.Language) bool {
	for i := 0; i < statement.NamedChildCount(); i++ {
		child := statement.NamedChild(i)
		if child == nil {
			continue
		}
		switch child.Type(language) {
		case "except_clause", "finally_clause":
			return true
		}
	}
	return false
}

func resolveEntry(ctx context.Context, langName, filename string, src []byte) *grammars.LangEntry {
	if name := strings.TrimSpace(langName); name != "" {
		return grammars.DetectLanguageByName(name)
	}
	return filekind.Detect(ctx, filekind.DetectReq{
		Filename:   filename,
		HeadSample: src,
		Mode:       filekind.DepthShallow,
	}).Grammar
}

func supported(name string) bool {
	for _, candidate := range filekind.SupportedLanguages() {
		if candidate == name {
			return true
		}
	}
	return false
}

type fault struct {
	node  *gotreesitter.Node
	start int
	end   int
	kind  string
}

func collectLeafFaults(root *gotreesitter.Node) []fault {
	var out []fault
	var walk func(*gotreesitter.Node) bool
	walk = func(node *gotreesitter.Node) bool {
		if node == nil || len(out) >= maxCollectedFaults {
			return false
		}
		childFault := false
		for i := 0; i < node.ChildCount(); i++ {
			if walk(node.Child(i)) {
				childFault = true
			}
		}
		isFault := node.IsError() || node.IsMissing()
		if isFault && !childFault {
			kind := DiagnosticError
			if node.IsMissing() {
				kind = DiagnosticMissing
			}
			out = append(out, fault{
				node: node, start: int(node.StartByte()), end: int(node.EndByte()), kind: kind,
			})
		}
		return isFault || childFault
	}
	walk(root)
	return out
}

func faultBurden(faults []fault) Burden {
	spans := make([][2]int, 0, len(faults))
	for _, item := range faults {
		start, end := item.start, item.end
		if end <= start {
			end = start + 1
		}
		spans = append(spans, [2]int{start, end})
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i][0] == spans[j][0] {
			return spans[i][1] < spans[j][1]
		}
		return spans[i][0] < spans[j][0]
	})
	covered := 0
	for i := 0; i < len(spans); {
		start, end := spans[i][0], spans[i][1]
		i++
		for i < len(spans) && spans[i][0] <= end {
			if spans[i][1] > end {
				end = spans[i][1]
			}
			i++
		}
		covered += end - start
	}
	return Burden{SpanBytes: covered, Faults: len(faults)}
}

func buildDiagnostics(faults []fault, src []byte, language *gotreesitter.Language) ([]Diagnostic, bool) {
	lines := strings.Split(string(src), "\n")
	all := make([]Diagnostic, 0, len(faults))
	for _, item := range faults {
		start, end := item.node.StartPoint(), item.node.EndPoint()
		row := int(start.Row) + 1
		var snippet string
		if row >= 1 && row <= len(lines) {
			snippet = runeclamp.ClampBytes(strings.TrimSpace(lines[row-1]), syntaxSnippetMaxLen)
		}
		all = append(all, Diagnostic{
			Row: row, Col: int(start.Column) + 1,
			EndRow: int(end.Row) + 1, EndCol: int(end.Column) + 1,
			StartByte: item.start, EndByte: item.end,
			Snippet: snippet, Kind: item.kind, NodeType: item.node.Type(language),
		})
	}
	sort.SliceStable(all, func(i, j int) bool {
		leftSpan := all[i].EndByte - all[i].StartByte
		rightSpan := all[j].EndByte - all[j].StartByte
		if leftSpan == rightSpan {
			if all[i].Row == all[j].Row {
				return all[i].Col < all[j].Col
			}
			return all[i].Row < all[j].Row
		}
		return leftSpan < rightSpan
	})
	all = dedupeDiagnosticRows(all)
	truncated := len(all) > maxDiagnostics || len(faults) >= maxCollectedFaults
	if len(all) > maxDiagnostics {
		all = all[:maxDiagnostics]
	}
	return all, truncated
}

func dedupeDiagnosticRows(in []Diagnostic) []Diagnostic {
	seen := make(map[int]struct{}, len(in))
	out := in[:0]
	for _, item := range in {
		if _, exists := seen[item.Row]; exists {
			continue
		}
		seen[item.Row] = struct{}{}
		out = append(out, item)
	}
	return out
}
