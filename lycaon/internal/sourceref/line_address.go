package sourceref

import (
	"regexp"
	"strconv"
)

type LineAddress struct {
	Path          string
	Line, EndLine int
}

var lineAddressTail = regexp.MustCompile(`(?::|#L?)([0-9]+)(?:-L?([0-9]+))?$`)

func ParseLineAddress(value string) (LineAddress, bool) {
	match := lineAddressTail.FindStringSubmatchIndex(value)
	if match == nil {
		return LineAddress{}, false
	}
	address := LineAddress{Path: value[:match[0]]}
	address.Line, _ = strconv.Atoi(value[match[2]:match[3]])
	if match[4] >= 0 {
		address.EndLine, _ = strconv.Atoi(value[match[4]:match[5]])
	}
	return address, true
}
