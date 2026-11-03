package literal

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// volumeStats reads the volume holding an open file at path.
type volumeStats func(file *os.File, path string) (fseffect.Volume, error)

// dfColumn is one df column: its header, minimum width and alignment.
type dfColumn struct {
	header string
	width  int
	left   bool
}

// runDf reports rows for the volumes holding the named paths, or for the
// attached folders and session scratch. Every path binds through the read
// authority first; the volume is read through the bound entry.
func runDf(inv *invocation) error {
	operands, defaults := inv.operands(), false
	if len(operands) == 0 {
		operands, defaults = inv.dfDefaults(), true
	}
	var rows []fseffect.Volume
	seen := map[string]bool{}
	for _, operand := range operands {
		vol, ok, err := inv.volumeOf(operand)
		if err != nil {
			return err
		}
		if !ok || defaults && seen[vol.Target] {
			continue
		}
		seen[vol.Target] = true
		rows = append(rows, vol)
	}
	if len(rows) == 0 {
		return nil
	}
	return inv.out.text(dfTable(rows, inv.flag("-i"), inv.flag("-h") && lastOf(inv, "-h", "-k") == "-h", inv.flag("-P")))
}

// dfDefaults are the attached folders and session scratch, in that order.
func (inv *invocation) dfDefaults() []string {
	var out []string
	for _, root := range inv.tc.Source.Roots {
		out = append(out, filepath.Clean(root.Path))
	}
	if dir := inv.tc.Host.SessionScratchDir; dir != "" {
		out = append(out, filepath.Clean(dir))
	}
	return out
}

// volumeOf binds one path and reads its volume through the bound entry.
func (inv *invocation) volumeOf(operand string) (fseffect.Volume, bool, error) {
	bound, err := inv.paths.Bind(inv.ctx, operand, false)
	var st fseffect.EntryStat
	if err == nil {
		st, err = bound.Stat(true)
	}
	var file *os.File
	switch {
	case err != nil:
	case typeLetter(st.Mode) == 'd':
		file, err = bound.OpenLaunchMetadata(true)
	default:
		file, err = bound.Open()
	}
	if err != nil {
		err = projectpaths.PathOperationError(operand, err)
		if reason, ok := ordinary(err); ok {
			inv.fail(1, operand, reason)
			return fseffect.Volume{}, false, nil
		}
		return fseffect.Volume{}, false, err
	}
	defer func() { _ = file.Close() }()
	vol, err := inv.owners.volumes(file, bound.Resolved.Abs)
	if errors.Is(err, fseffect.ErrUnsupported) {
		return vol, false, platformUnavailable("df", "file system statistics")
	}
	return vol, err == nil, err
}

// dfTable lays out the columns: each is as wide as its header, its widest
// cell and its minimum; Filesystem aligns left, Mounted on is last.
func dfTable(rows []fseffect.Volume, inodes, human, posix bool) string {
	columns := []dfColumn{{header: "Filesystem", width: 14, left: true}}
	switch {
	case inodes:
		columns = append(columns, dfColumn{header: "Inodes", width: 5}, dfColumn{header: "IUsed", width: 5}, dfColumn{header: "IFree", width: 5}, dfColumn{header: "IUse%", width: 4})
	default:
		size, percent := "1K-blocks", "Use%"
		if human {
			size = "Size"
		} else if posix {
			size = "1024-blocks"
		}
		if posix {
			percent = "Capacity"
		}
		used, avail := "Used", "Available"
		if human {
			avail = "Avail"
		}
		columns = append(columns, dfColumn{header: size, width: 5}, dfColumn{header: used, width: 5}, dfColumn{header: avail, width: 5}, dfColumn{header: percent, width: 4})
	}
	columns = append(columns, dfColumn{header: "Mounted on", left: true})
	table := [][]string{make([]string, len(columns))}
	for i, column := range columns {
		table[0][i] = column.header
	}
	for _, vol := range rows {
		table = append(table, dfCells(vol, inodes, human))
	}
	for _, row := range table {
		for i, cell := range row {
			columns[i].width = max(columns[i].width, len(cell))
		}
	}
	var out strings.Builder
	for _, row := range table {
		for i, cell := range row {
			if i > 0 {
				out.WriteByte(' ')
			}
			pad := strings.Repeat(" ", columns[i].width-len(cell))
			switch {
			case i == len(row)-1:
				out.WriteString(cell)
			case columns[i].left:
				out.WriteString(cell + pad)
			default:
				out.WriteString(pad + cell)
			}
		}
		out.WriteByte('\n')
	}
	return out.String()
}

// dfCells renders one row: block columns in KiB rounded up (or -h sizes),
// inode columns as counts, percentages rounded up, unknown ratios as -.
func dfCells(v fseffect.Volume, inodes, human bool) []string {
	total, used, avail := v.Blocks*v.BlockSize, (v.Blocks-min(v.Free, v.Blocks))*v.BlockSize, v.Available*v.BlockSize
	render := func(bytes uint64) string { return strconv.FormatUint((bytes+1023)/1024, 10) }
	if inodes {
		total, used, avail = v.Files, v.Files-min(v.FreeFiles, v.Files), v.FreeFiles
		render = func(count uint64) string { return strconv.FormatUint(count, 10) }
	}
	if human {
		render = func(n uint64) string { return humanSize(int64(n)) }
	}
	percent := "-"
	if used+avail > 0 {
		percent = strconv.FormatFloat(math.Ceil(float64(used)*100/float64(used+avail)), 'f', 0, 64) + "%"
	}
	return []string{v.Source, render(total), render(used), render(avail), percent, v.Target}
}

// lastOf is whichever of the given option keys appears last in argv.
func lastOf(inv *invocation, keys ...string) string {
	last := ""
	for _, item := range inv.argv.Items {
		if item.Option == nil {
			continue
		}
		for _, key := range keys {
			if item.Option.Key == key {
				last = key
			}
		}
	}
	return last
}
