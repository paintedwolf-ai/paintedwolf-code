package report

import (
	"github.com/johnfercher/go-tree/node"
	"github.com/johnfercher/maroto/v2/pkg/consts/align"
	"github.com/johnfercher/maroto/v2/pkg/core"
	"github.com/johnfercher/maroto/v2/pkg/core/entity"
	"github.com/johnfercher/maroto/v2/pkg/props"
)

// rightText is one line set flush right in its cell: a page number, a count.
// It never wraps, so its height is the line's whatever the text.
type rightText struct {
	text  string
	prop  props.Text
	width float64
	ms    *measurer
}

func newRightText(ms *measurer, text string, prop props.Text, width float64) *rightText {
	return &rightText{text: text, prop: prop, width: width, ms: ms}
}

func (r *rightText) SetConfig(*entity.Config) {}

func (r *rightText) GetHeight(core.Provider, *entity.Cell) float64 {
	return r.ms.fontHeight(r.prop.Size) + r.prop.Top + r.prop.Bottom
}

func (r *rightText) GetStructure() *node.Node[core.Structure] {
	return node.New(core.Structure{Type: "inline_text", Value: r.text})
}

func (r *rightText) Render(provider core.Provider, cell *entity.Cell) {
	if r.text == "" {
		return
	}
	key := styleKey{family: r.prop.Family, style: r.prop.Style, size: r.prop.Size}
	w := r.ms.width(r.text, key) + fragmentSlack
	prop := r.prop
	prop.Top, prop.Bottom, prop.Left, prop.Right = 0, 0, 0, 0
	prop.VerticalPadding = 0
	prop.Align = align.Left
	provider.AddText(r.text, &entity.Cell{
		X:      cell.X + r.width - w,
		Y:      cell.Y + r.prop.Top,
		Width:  w,
		Height: r.ms.fontHeight(r.prop.Size),
	}, &prop)
}
