package report

import (
	"strings"

	"github.com/johnfercher/go-tree/node"
	"github.com/johnfercher/maroto/v2/pkg/components/col"
	"github.com/johnfercher/maroto/v2/pkg/components/row"
	"github.com/johnfercher/maroto/v2/pkg/consts/align"
	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
	"github.com/johnfercher/maroto/v2/pkg/consts/linestyle"
	"github.com/johnfercher/maroto/v2/pkg/consts/orientation"
	"github.com/johnfercher/maroto/v2/pkg/core"
	"github.com/johnfercher/maroto/v2/pkg/core/entity"
	"github.com/johnfercher/maroto/v2/pkg/props"
)

// Chips use a line as their background.

type chipLabel struct {
	text  string
	width float64
	tone  chipTone
}

// newTonedChip sets a status word: sentence case, since the host writes it for
// a reader. An identifier keeps its own spelling — see newLiteralChip.
func newTonedChip(ms *measurer, text string, tone chipTone) chipLabel {
	return newLiteralChip(ms, chipText(text), tone)
}

// newLiteralChip sets a name rather than a word: humanizing a claim id would
// turn `app-1` into prose it never was.
func newLiteralChip(ms *measurer, text string, tone chipTone) chipLabel {
	text = strings.TrimSpace(text)
	if text == "" {
		return chipLabel{}
	}
	w := ms.width(text, chipStyle()) + 2*chipPadX
	if w < chipMinWid {
		w = chipMinWid
	}
	return chipLabel{text: text, width: w, tone: tone}
}

// chipText renders sentence-case labels.
func chipText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return humanize(strings.ToLower(text))
}

func chipStyle() styleKey {
	return styleKey{family: familySans, style: fontstyle.Bold, size: sizeChip}
}

func chipHeight(ms *measurer) float64 {
	return ms.fontHeight(sizeChip) + 2*chipPadY
}

type chips struct {
	ms     *measurer
	lines  [][]chipLabel
	top    float64
	bottom float64
}

func newChips(ms *measurer, labels []chipLabel, width float64) *chips {
	c := &chips{ms: ms}
	var cur []chipLabel
	x := 0.0
	for _, l := range labels {
		if l.text == "" {
			continue
		}
		if len(cur) > 0 && x+chipGap+l.width > width {
			c.lines = append(c.lines, cur)
			cur, x = nil, 0
		}
		if len(cur) > 0 {
			x += chipGap
		}
		cur = append(cur, l)
		x += l.width
	}
	if len(cur) > 0 {
		c.lines = append(c.lines, cur)
	}
	return c
}

func (c *chips) height() float64 {
	n := float64(len(c.lines))
	if n == 0 {
		return 0
	}
	return n*chipHeight(c.ms) + (n-1)*chipPadY + c.top + c.bottom
}

func (c *chips) SetConfig(*entity.Config) {}

func (c *chips) GetHeight(core.Provider, *entity.Cell) float64 { return c.height() }

func (c *chips) GetStructure() *node.Node[core.Structure] {
	var b strings.Builder
	for _, line := range c.lines {
		for _, l := range line {
			b.WriteString(l.text)
			b.WriteString(" ")
		}
	}
	return node.New(core.Structure{Type: "chips", Value: strings.TrimSpace(b.String())})
}

func (c *chips) Render(provider core.Provider, cell *entity.Cell) {
	h := chipHeight(c.ms)
	pitch := h + chipPadY

	for i, line := range c.lines {
		y := cell.Y + c.top + float64(i)*pitch
		x := cell.X
		for _, l := range line {
			tone := l.tone
			if tone.fill == nil {
				tone = toneNeutral
			}
			provider.AddLine(&entity.Cell{X: x, Y: y, Width: l.width, Height: h}, &props.Line{
				Color:         tone.fill,
				Style:         linestyle.Solid,
				Thickness:     h,
				Orientation:   orientation.Horizontal,
				OffsetPercent: 50,
				SizePercent:   100,
			})
			provider.AddText(l.text, &entity.Cell{
				X:      x + chipPadX,
				Y:      y + chipPadY,
				Width:  l.width,
				Height: c.ms.fontHeight(sizeChip),
			}, &props.Text{
				Family: familySans,
				Style:  fontstyle.Bold,
				Size:   sizeChip,
				Color:  tone.ink,
				Align:  align.Left,
			})
			x += l.width + chipGap
		}
	}
}

func chipStripRow(ms *measurer, labels []chipLabel, top, bottom float64) measuredRow {
	c := newChips(ms, labels, contentWidth)
	c.top, c.bottom = top, bottom
	h := c.height()
	if h == 0 {
		return spacerRow(0)
	}
	return measuredRow{row: row.New(h).Add(col.New(gridSize).Add(c)), height: h}
}
