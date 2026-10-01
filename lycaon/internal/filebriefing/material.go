package filebriefing

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/summarize"
)

// Input is one host-resolved file presentation.
type Input struct {
	Path, Presentation string
	Source             string
	SourceSHA256       string
}

// Material pairs immediate structure with bounded model evidence.
type Material struct {
	Preview   Preview
	Locations []Location
	Prompt    string
}

type fileFacts struct {
	Path              string `json:"path"`
	Language          string `json:"language,omitempty"`
	PhysicalLines     int    `json:"physical_lines"`
	Bytes             int    `json:"bytes"`
	DeclarationsTotal int    `json:"declarations_total"`
	OutlineSource     string `json:"outline_source,omitempty"`
}

type sourceRange struct {
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Symbol    string `json:"symbol,omitempty"`
}

type materialCoverage struct {
	SelectionMethod      string        `json:"selection_method"`
	SourceComplete       bool          `json:"source_complete"`
	OutlineComplete      bool          `json:"outline_complete"`
	LeadingExcerpt       sourceRange   `json:"leading_excerpt"`
	LeadingTruncated     bool          `json:"leading_excerpt_truncated"`
	SelectedSymbolRanges []sourceRange `json:"selected_symbol_ranges"`
	DeclarationsSupplied int           `json:"declarations_supplied"`
	DeclarationsTotal    int           `json:"declarations_total"`
}

type leadingExcerpt struct {
	Text                string
	EndLine             int
	CompleteThroughLine int
	Complete            bool
}

func BuildMaterial(ctx context.Context, input Input, cfg Config) (Material, error) {
	// Budget estimates share the summary tokenizer.
	caps := summarize.DefaultCaps()
	caps.Gather.SymbolWindowLines = cfg.Material.SymbolWindowLines
	caps.Pack.WireBudgetTokens = 0

	outline := fileoutline.AnalyzeText(ctx, input.Path, []byte(input.Source))
	outlineBudget := cfg.Material.PromptBudgetTokens - cfg.Material.HeaderBudgetTokens -
		cfg.Material.EnvelopeReserveTokens - cfg.Material.MinimumPackTokens
	outlineSymbols := fitOutlineSymbols(caps, outline.Symbols, cfg.Material.OutlineSymbols, outlineBudget)
	outlineInventory := renderOutlineInventory(outlineSymbols, len(outline.Symbols))
	selectionSymbols := sampleOutlineSymbols(outline.Symbols, min(6, cfg.Material.OutlineSymbols))
	header := numberedHeader(input.Source, cfg.Material.HeaderLines, cfg.Material.HeaderBudgetTokens)
	reserve := caps.EstimateTokens(header.Text) + caps.EstimateTokens(outlineInventory) + cfg.Material.EnvelopeReserveTokens
	caps.Pack.InputBudgetTokens = cfg.Material.PromptBudgetTokens - reserve
	if caps.Pack.InputBudgetTokens < cfg.Material.MinimumPackTokens {
		return Material{}, fmt.Errorf("file briefing material budget is below minimum pack size")
	}

	result, err := summarize.BriefContent(ctx, summarize.ContentInput{
		Path: input.Path, Content: input.Source,
		Task: selectionTask(cfg.Task, selectionSymbols), Analysis: outline,
	}, caps)
	if err != nil {
		return Material{}, err
	}
	facts := fileFacts{
		Path: input.Path, Language: outline.Language, PhysicalLines: physicalLineCount(input.Source),
		Bytes: len(input.Source), DeclarationsTotal: len(outline.Symbols), OutlineSource: outline.Source,
	}
	preview := Preview{Language: outline.Language, LineCount: facts.PhysicalLines}
	coverage := coverageForMaterial(outline, facts, header, outlineSymbols, result.Pack)
	return Material{
		Preview:   preview,
		Locations: locationsFromOutline(outlineSymbols),
		Prompt:    renderPrompt(input, header, facts, coverage, outlineInventory, result.Pack),
	}, nil
}

func physicalLineCount(source string) int {
	if source == "" {
		return 0
	}
	count := strings.Count(source, "\n") + 1
	if strings.HasSuffix(source, "\n") {
		count--
	}
	return count
}

func sampleOutlineSymbols(symbols []fileoutline.Symbol, limit int) []fileoutline.Symbol {
	if limit <= 1 {
		return boundOutlineSymbols(symbols[:min(len(symbols), limit)])
	}
	if len(symbols) <= limit {
		return boundOutlineSymbols(symbols)
	}
	out := make([]fileoutline.Symbol, 0, limit)
	for i := 0; i < limit; i++ {
		at := i * (len(symbols) - 1) / (limit - 1)
		symbol := symbols[at]
		symbol.Name = boundedSymbolName(symbol.Name)
		out = append(out, symbol)
	}
	return out
}

func boundOutlineSymbols(symbols []fileoutline.Symbol) []fileoutline.Symbol {
	out := append([]fileoutline.Symbol(nil), symbols...)
	for index := range out {
		out[index].Name = boundedSymbolName(out[index].Name)
	}
	return out
}

func fitOutlineSymbols(caps summarize.Caps, symbols []fileoutline.Symbol, limit, budget int) []fileoutline.Symbol {
	for count := min(len(symbols), limit); count > 0; count-- {
		sampled := sampleOutlineSymbols(symbols, count)
		if caps.EstimateTokens(renderOutlineInventory(sampled, len(symbols))) <= budget {
			return sampled
		}
	}
	return nil
}

func renderOutlineInventory(symbols []fileoutline.Symbol, total int) string {
	var out strings.Builder
	out.WriteString("<outline_inventory_jsonl>\n")
	for _, symbol := range symbols {
		fmt.Fprintf(&out, `{"kind":%s,"name":%s,"line":%d}`+"\n", jsonString(symbol.Kind), jsonString(symbol.Name), symbol.Line)
	}
	if omitted := total - len(symbols); omitted > 0 {
		fmt.Fprintf(&out, `{"omitted_declarations":%d}`+"\n", omitted)
	}
	out.WriteString("</outline_inventory_jsonl>")
	return out.String()
}

func boundedSymbolName(name string) string {
	const maxRunes = 160
	return runeclamp.Clamp(name, maxRunes)
}

func selectionTask(task string, symbols []fileoutline.Symbol) string {
	var out strings.Builder
	out.WriteString(task)
	for _, symbol := range symbols {
		if name := strings.TrimSpace(symbol.Name); name != "" {
			out.WriteByte(' ')
			out.WriteString(name)
		}
	}
	return out.String()
}

func coverageForMaterial(outline fileoutline.Result, facts fileFacts, header leadingExcerpt, outlineSymbols []fileoutline.Symbol, pack summarize.ContextPack) materialCoverage {
	method := outline.Source + "_stratified_symbol_windows"
	if outline.Source == "" {
		method = "bounded_structural_fallback"
	}
	headerRange := sourceRange{}
	if header.EndLine > 0 {
		headerRange = sourceRange{StartLine: 1, EndLine: header.EndLine}
	}
	coverage := materialCoverage{
		SelectionMethod:      method,
		OutlineComplete:      len(outlineSymbols) == facts.DeclarationsTotal,
		LeadingExcerpt:       headerRange,
		LeadingTruncated:     !header.Complete,
		DeclarationsSupplied: len(outlineSymbols), DeclarationsTotal: facts.DeclarationsTotal,
	}
	covered := []sourceRange(nil)
	if header.CompleteThroughLine > 0 {
		covered = append(covered, sourceRange{StartLine: 1, EndLine: header.CompleteThroughLine})
	}
	for _, window := range pack.Substance {
		range_ := sourceRange{
			StartLine: max(1, window.StartLine), EndLine: min(facts.PhysicalLines, window.EndLine), Symbol: window.Symbol,
		}
		coverage.SelectedSymbolRanges = append(coverage.SelectedSymbolRanges, range_)
		covered = append(covered, range_)
	}
	coverage.SourceComplete = rangesCoverSource(covered, facts.PhysicalLines)
	return coverage
}

func rangesCoverSource(ranges []sourceRange, lineCount int) bool {
	if lineCount == 0 {
		return true
	}
	coveredThrough := 0
	for coveredThrough < lineCount {
		advanced := false
		for _, range_ := range ranges {
			if range_.StartLine <= coveredThrough+1 && range_.EndLine > coveredThrough {
				coveredThrough = range_.EndLine
				advanced = true
			}
		}
		if !advanced {
			return false
		}
	}
	return true
}

func locationsFromOutline(symbols []fileoutline.Symbol) []Location {
	out := make([]Location, 0, len(symbols))
	for _, symbol := range symbols {
		name := strings.TrimSpace(symbol.Name)
		if name == "" || symbol.Line < 1 {
			continue
		}
		out = append(out, Location{Line: symbol.Line, Name: name, Kind: strings.TrimSpace(symbol.Kind)})
	}
	return out
}

func renderPrompt(input Input, header leadingExcerpt, facts fileFacts, coverage materialCoverage, outlineInventory string, pack summarize.ContextPack) string {
	var out strings.Builder
	fmt.Fprintf(&out, "File: %s\nPresentation: %s\nSource SHA-256: %s\n", jsonString(input.Path), jsonString(input.Presentation), jsonString(input.SourceSHA256))
	fmt.Fprintf(&out, "Authoritative file facts: %s\nMaterial coverage: %s\n", jsonString(facts), jsonString(coverage))
	out.WriteString(outlineInventory)
	out.WriteString("\n\n")
	out.WriteString("<leading_source_excerpt_json>\n")
	headerStart := 0
	if header.EndLine > 0 {
		headerStart = 1
	}
	out.WriteString(jsonString(struct {
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
		Complete  bool   `json:"complete"`
		Content   string `json:"content"`
	}{headerStart, header.EndLine, header.Complete, header.Text}))
	out.WriteString("\n</leading_source_excerpt_json>\n\n<context_pack_jsonl>\n")
	for _, window := range pack.Substance {
		fmt.Fprintf(&out, `{"type":"window","symbol":%s,"start_line":%d,"end_line":%d,"body":%s}`+"\n", jsonString(window.Symbol), window.StartLine, window.EndLine, jsonString(window.Body))
	}
	for _, edge := range pack.Imports {
		fmt.Fprintf(&out, `{"type":"dependency","kind":%s,"from":%s,"to":%s}`+"\n", jsonString(edge.Kind), jsonString(edge.From), jsonString(edge.To))
	}
	out.WriteString("</context_pack_jsonl>")
	return out.String()
}

func jsonString(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("encode JSON value: %v", err))
	}
	return string(encoded)
}

func numberedHeader(source string, maxLines, maxTokens int) leadingExcerpt {
	if source == "" {
		return leadingExcerpt{Complete: true}
	}
	lines := strings.Split(source, "\n")
	if strings.HasSuffix(source, "\n") {
		lines = lines[:len(lines)-1]
	}
	allLines := len(lines)
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	remaining := maxTokens * 4
	var out strings.Builder
	endLine := 0
	completeThroughLine := 0
	complete := len(lines) == allLines
	for i, line := range lines {
		prefix := fmt.Sprintf("%d: ", i+1)
		if remaining <= utf8.RuneCountInString(prefix)+1 {
			break
		}
		out.WriteString(prefix)
		endLine = i + 1
		remaining -= utf8.RuneCountInString(prefix)
		truncated := false
		for _, r := range line {
			if remaining <= 2 {
				truncated = true
				break
			}
			out.WriteRune(r)
			remaining--
		}
		if truncated {
			out.WriteRune('…')
			remaining--
		}
		out.WriteByte('\n')
		remaining--
		if truncated {
			complete = false
			break
		}
		completeThroughLine = i + 1
	}
	return leadingExcerpt{
		Text: strings.TrimSuffix(out.String(), "\n"), EndLine: endLine,
		CompleteThroughLine: completeThroughLine, Complete: complete,
	}
}
