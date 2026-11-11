package query

import (
	"fmt"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	nativejq "github.com/lycaon/lycaon/internal/tools/native/jq"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

func (in *inputs) open(operand commandprogram.Field) {
	in.close()
	in.label = operand.Value
	in.text = ""
	in.lastLine = 0
	if !in.yaml {
		in.openStream(operand)
		return
	}
	if operand.Value == "-" {
		in.label = "<stdin>"
		text, err := in.captureYAMLInput(in.inv.input)
		if err != nil {
			in.stop = err
			return
		}
		in.text = text
	} else {
		captured := in.captured
		if captured == nil {
			var err error
			captured, err = in.inv.capture(operand)
			if err != nil {
				in.stop = err
				return
			}
		}
		if captured == nil {
			in.openErrors = true
			return
		}
		in.text = captured.text
	}
	if in.raw {
		in.pending = rawLines(in.text)
		return
	}
	if strings.TrimSpace(in.text) == "" {
		return
	}
	values, _, err := nativejq.DecodeDocuments([]byte(in.text), "yaml")
	if err != nil {
		_, _ = fmt.Fprintf(in.inv.stderr, "%s: error: %s (while parsing %s)\n", in.inv.name(), err, in.label)
		in.broken = true
		return
	}
	for _, value := range values {
		if !in.admit(value) {
			return
		}
	}
	if in.inv.flag("-Y") {
		if in.roundtrip == nil {
			in.roundtrip = &nativejq.YAMLRoundtrip{}
		}
		in.roundtrip.Add(in.text)
	}
	in.pending = values
}
func (in *inputs) captureYAMLInput(reader io.Reader) (string, error) {
	buffer, err := in.inv.owners.Revisions.ReadWholeInput(in.inv.ctx, io.LimitReader(reader, in.limit+1), in.limit)
	if err != nil {
		return "", err
	}
	defer buffer.Close()
	if int64(len(buffer.Bytes)) > in.limit {
		return "", safecmd.Reject("JQ_INPUT_TOO_LARGE", map[string]any{"path": in.label, "max_bytes": in.limit})
	}
	work := &queryWorking{inv: in.inv}
	if err := work.resize(int64(len(buffer.Bytes))); err != nil {
		return "", err
	}
	in.inv.releases = append(in.inv.releases, work.Close)
	return string(buffer.Bytes), nil
}
func rawLines(text string) []any {
	var result []any
	for len(text) > 0 {
		line, rest, found := strings.Cut(text, "\n")
		result = append(result, line)
		if !found {
			break
		}
		text = rest
	}
	return result
}
