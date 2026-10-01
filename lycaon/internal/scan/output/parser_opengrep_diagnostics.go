package output

import (
	"encoding/json"
	"fmt"
)

func (e *opengrepJSONErr) UnmarshalJSON(raw []byte) error {
	type plain opengrepJSONErr
	wire := struct {
		*plain
		Type  json.RawMessage `json:"type"`
		Spans []struct {
			File  string `json:"file"`
			Start struct {
				Line   int `json:"line"`
				Column int `json:"col"`
			} `json:"start"`
		} `json:"spans"`
	}{plain: (*plain)(e)}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	if len(wire.Type) > 0 {
		if err := e.Type.UnmarshalJSON(wire.Type); err != nil {
			return err
		}
	}
	if e.Type != "PartialSemantics" && e.Type != "PartialParsing" {
		return nil
	}
	if e.Type == "PartialSemantics" {
		var tagged []json.RawMessage
		if err := json.Unmarshal(wire.Type, &tagged); err != nil || len(tagged) != 2 {
			return fmt.Errorf("opengrep semantic diagnostic requires a construct code")
		}
		if err := json.Unmarshal(tagged[1], &e.Construct); err != nil || e.Construct == "" {
			return fmt.Errorf("opengrep semantic diagnostic has an invalid construct code")
		}
		if len(wire.Spans) > 1 {
			return fmt.Errorf("opengrep semantic diagnostic has ambiguous locations")
		}
	}
	for _, span := range wire.Spans {
		if span.File == "" || span.Start.Line < 1 || span.Start.Column < 1 || (e.Path != "" && e.Path != span.File) {
			return fmt.Errorf("opengrep diagnostic has an invalid location")
		}
		e.Path = span.File
		if e.StartLine == 0 || span.Start.Line < e.StartLine || span.Start.Line == e.StartLine && span.Start.Column < e.StartColumn {
			e.StartLine, e.StartColumn = span.Start.Line, span.Start.Column
		}
	}
	return nil
}
