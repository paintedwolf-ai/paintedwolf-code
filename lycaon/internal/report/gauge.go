package report

import (
	"github.com/johnfercher/go-tree/node"
	"github.com/johnfercher/maroto/v2/pkg/consts/linestyle"
	"github.com/johnfercher/maroto/v2/pkg/consts/orientation"
	"github.com/johnfercher/maroto/v2/pkg/core"
	"github.com/johnfercher/maroto/v2/pkg/core/entity"
	"github.com/johnfercher/maroto/v2/pkg/props"
)

// Gauge geometry, in millimetres.
const (
	gaugeBarHeight = 2.2
	gaugeBarGap    = 0.9
	gaugePadTop    = 2.0
	gaugePadBottom = 1.4
)

// scaleBar draws a scale as equal segments, left to right. Segments up to
// firm are solid in the tone's ink; those up to soft are its lighter ground,
// for a range an open answer could still reach; the rest are empty.
type scaleBar struct {
	steps int
	firm  int
	soft  int
	tone  chipTone
}

func (s *scaleBar) height() float64 { return gaugePadTop + gaugeBarHeight + gaugePadBottom }

func (s *scaleBar) SetConfig(*entity.Config) {}

func (s *scaleBar) GetHeight(core.Provider, *entity.Cell) float64 { return s.height() }

func (s *scaleBar) GetStructure() *node.Node[core.Structure] {
	return node.New(core.Structure{Type: "scale"})
}

func (s *scaleBar) Render(provider core.Provider, cell *entity.Cell) {
	if s.steps <= 0 {
		return
	}
	width := (cell.Width - float64(s.steps-1)*gaugeBarGap) / float64(s.steps)
	y := cell.Y + gaugePadTop
	for i := 0; i < s.steps; i++ {
		color := hairlineColor
		switch {
		case i < s.firm:
			color = s.tone.ink
		case i < s.soft:
			color = s.tone.fill
		}
		provider.AddLine(&entity.Cell{
			X: cell.X + float64(i)*(width+gaugeBarGap), Y: y, Width: width, Height: gaugeBarHeight,
		}, &props.Line{
			Color:         color,
			Style:         linestyle.Solid,
			Thickness:     gaugeBarHeight,
			Orientation:   orientation.Horizontal,
			OffsetPercent: 50,
			SizePercent:   100,
		})
	}
}

// scaleCell is a scale bar as a cell of a band.
func scaleCell(width float64, bar *scaleBar) cellSpec {
	return cellSpec{comp: bar, width: width, height: bar.height()}
}

// stack sets cells one above another inside a single band cell, so two
// columns of differently sized parts sit side by side.
type stack struct {
	cells []cellSpec
}

func (s *stack) height() float64 {
	var h float64
	for _, c := range s.cells {
		h += c.height
	}
	return h
}

func (s *stack) SetConfig(*entity.Config) {}

func (s *stack) GetHeight(core.Provider, *entity.Cell) float64 { return s.height() }

func (s *stack) GetStructure() *node.Node[core.Structure] {
	root := node.New(core.Structure{Type: "stack"})
	for _, c := range s.cells {
		root.AddNext(c.comp.GetStructure())
	}
	return root
}

func (s *stack) Render(provider core.Provider, cell *entity.Cell) {
	y := cell.Y
	for _, c := range s.cells {
		c.comp.Render(provider, &entity.Cell{X: cell.X, Y: y, Width: c.width, Height: c.height})
		y += c.height
	}
}

// stackCell stacks cells into one column of the given width.
func stackCell(width float64, cells []cellSpec) cellSpec {
	s := &stack{cells: cells}
	return cellSpec{comp: s, width: width, height: s.height()}
}
