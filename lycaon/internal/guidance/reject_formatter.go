package guidance

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
)

// StaticRejectFormatter renders catalog entries as structured rejections.
type StaticRejectFormatter struct {
	hints *HintConfig
}

func NewStaticRejectFormatter(hints *HintConfig) *StaticRejectFormatter {
	return &StaticRejectFormatter{hints: hints}
}

// Format renders the reject block for a guidance code.
func (f *StaticRejectFormatter) Format(code string, data map[string]any) (string, error) {
	entry, ok := f.lookup(code)
	if !ok {
		return "", fmt.Errorf("unknown guidance code %q", code)
	}
	if err := entryCoversTool(code, entry, data); err != nil {
		return "", err
	}
	block, err := RenderUnifiedRejectBlock(context.Background(), code, entry, data)
	if err != nil {
		return "", err
	}
	if block == "" {
		return "", fmt.Errorf("empty reject render for %q", code)
	}
	return block, nil
}

// Parse extracts fields from a reject block.
func (f *StaticRejectFormatter) Parse(raw string) (*RejectBlock, error) {
	block := &RejectBlock{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, hostmarker.Rejected):
			block.What = strings.TrimSpace(strings.TrimPrefix(line, hostmarker.Rejected))
		case strings.HasPrefix(line, "Cause:"):
			block.Cause = strings.TrimSpace(strings.TrimPrefix(line, "Cause:"))
		case strings.HasPrefix(line, "Fix:"):
			block.Fix = strings.TrimSpace(strings.TrimPrefix(line, "Fix:"))
		case strings.HasPrefix(line, "Code:"):
			block.Code = strings.TrimSpace(strings.TrimPrefix(line, "Code:"))
		}
	}
	if block.Code == "" {
		return nil, fmt.Errorf("reject block missing Code")
	}
	return block, nil
}

// entryCoversTool validates a rejection card's tool and operation selectors.
// The operation is the host-attributed native operation, else the actual tool.
func entryCoversTool(code string, entry HintEntry, data map[string]any) error {
	tool, _ := data["tool"].(string)
	if tool == "" {
		return nil
	}
	if err := selectorCovers(code, entry.Tools, tool); err != nil {
		return err
	}
	operation, _ := data["native_operation"].(string)
	if operation == "" {
		operation = tool
	}
	return selectorCovers(code, entry.Operations(), operation)
}

func selectorCovers(code string, selected []string, value string) error {
	if len(selected) == 0 {
		return nil
	}
	for _, allowed := range selected {
		// [OAR-SEL-1] Selector membership uses exact string equality.
		if allowed == value {
			return nil
		}
	}
	return fmt.Errorf(
		"guidance code %q is authored for %v; refusing to render it for %q",
		code, selected, value,
	)
}

func (f *StaticRejectFormatter) lookup(code string) (HintEntry, bool) {
	if f == nil || f.hints == nil {
		return HintEntry{}, false
	}
	entry, ok := f.hints.HintCodes[code]
	return entry, ok
}
