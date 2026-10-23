package literal

import (
	"fmt"
	"strings"
	"testing"
)

func TestTailStreamsMatchSeekableWindowsAcrossChunkBoundaries(t *testing.T) {
	h := formHarness(t)
	content := strings.Repeat("a", 32767) + "\n" + strings.Repeat("b", 65537) + "\nc\nd"
	seed(t, h.root, map[string]string{"large": content})
	for _, option := range []struct {
		flag, count string
		window      window
	}{
		{"-n", "1", window{count: 1}}, {"-n", "2", window{count: 2}}, {"-n", "0", window{}},
		{"-n", "+2", window{count: 2, fromStart: true}},
		{"-c", "4", window{bytes: true, count: 4}}, {"-c", "+3", window{bytes: true, count: 3, fromStart: true}},
	} {
		want := content[tailStart([]byte(content), option.window):]
		for _, operand := range []string{"-", "large"} {
			input := ""
			if operand == "-" {
				input = content
			}
			h.exec(input, "tail", option.flag, option.count, operand).expect(t, fmt.Sprint(option, operand), want, "", 0)
		}
	}
}
