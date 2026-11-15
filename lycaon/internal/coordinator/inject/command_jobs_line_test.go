package inject

import (
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf8"
)

// Every ledger command is one line within the row bound, whatever the
// statement's shape; a short single-line command is shown as written.
func TestCommandLedgerLineIsOneBoundedLine(t *testing.T) {
	pieces := []string{"python3", " -m http.server 8000", "\n", "\r\n", "  ", "\t", "(", ") &", "é", "日本", "\r", "cd site &&"}
	random := rand.New(rand.NewPCG(1, 2))
	for range 2000 {
		var b strings.Builder
		for range random.IntN(40) {
			b.WriteString(pieces[random.IntN(len(pieces))])
		}
		command := b.String()
		line := commandLedgerLine(command)
		if strings.ContainsAny(line, "\r\n") || utf8.RuneCountInString(line) > maxCommandLedgerLineRunes {
			t.Fatalf("command %q became row %q", command, line)
		}
		if !strings.ContainsAny(command, "\r\n") && command == strings.TrimSpace(command) && utf8.RuneCountInString(command) <= maxCommandLedgerLineRunes && line != command {
			t.Fatalf("single-line command %q changed to %q", command, line)
		}
	}
}
