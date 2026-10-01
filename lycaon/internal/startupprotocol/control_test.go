package startupprotocol

import (
	"strings"
	"testing"
)

func TestReadControlShutdown(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		valid bool
	}{
		{name: "shutdown", input: "shutdown\n", valid: true},
		{name: "truncated", input: "shutdown", valid: false},
		{name: "unknown", input: "restart!\n", valid: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ReadControlShutdown(strings.NewReader(test.input))
			if (err == nil) != test.valid {
				t.Fatalf("ReadControlShutdown() error = %v, valid = %v", err, test.valid)
			}
		})
	}
}
