package report

import (
	"strconv"

	"github.com/johnfercher/maroto/v2/pkg/consts/align"
	"github.com/johnfercher/maroto/v2/pkg/props"
)

// Fixed-height placeholders preserve page positions.

type contentsEntry struct {
	title string
	level int
	page  int
}

const minPagesForContents = 3

const contentsPageWidth = 14.0

func contentsEntries(blocks []block, pages []int) []contentsEntry {
	var out []contentsEntry
	for i, b := range blocks {
		if b.contents == nil {
			continue
		}
		e := *b.contents
		if i < len(pages) {
			e.page = pages[i]
		}
		out = append(out, e)
	}
	return out
}

func contentsBlocks(ms *measurer, entries []contentsEntry) []block {
	if len(entries) == 0 {
		return nil
	}
	out := []block{sectionTitle(ms, sectionContents)}
	out[0].contents = nil

	titleProp := valueProp()
	titleProp.Size = sizeBody
	titleProp.VerticalPadding = leading(sizeBody)
	titleProp.Bottom = spaceBetweenListItem
	pageProp := titleProp
	pageProp.Color = mutedColor
	pageProp.Align = align.Right

	rows := make([]measuredRow, 0, len(entries))
	for _, e := range entries {
		tp := titleProp
		if e.level > 1 {
			tp.Left = listIndent
			tp.Color = mutedColor
		}
		page := ""
		if e.page > 0 {
			page = strconv.Itoa(e.page)
		}
		rows = append(rows, columnsRow([]cellSpec{
			newCell(ms, contentWidth-contentsPageWidth, []inlineRun{{Text: e.title}}, tp),
			newPageCell(ms, contentsPageWidth, page, pageProp),
		}))
	}
	rows = append(rows, spacerRow(spaceAfterParagraph))
	return append(out, rowsBlock(rows...))
}

// newPageCell reserves the final page-number height.
func newPageCell(ms *measurer, width float64, text string, prop props.Text) cellSpec {
	return cellSpec{
		comp:   newRightText(ms, text, prop, width),
		width:  width,
		height: ms.fontHeight(prop.Size) + prop.Top + prop.Bottom,
	}
}
