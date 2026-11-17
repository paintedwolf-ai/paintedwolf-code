package contract

import (
	"go/token"
	"strings"
)

func itoaLine(fset *token.FileSet, pos token.Pos) string {
	p := fset.Position(pos)
	return strings.TrimSpace(strings.TrimPrefix(p.String(), p.Filename+":"))
}
