package logscli

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
)

func printOutcomes(out io.Writer, path, sessionID string) error {
	if path == "" || sessionID == "" {
		return fmt.Errorf("outcomes requires --file and --session")
	}
	bundle, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer bundle.Close()
	entry, err := bundle.Open("action-outcomes.json")
	if err != nil {
		return err
	}
	defer entry.Close()
	decoder := json.NewDecoder(entry)
	if _, err := decoder.Token(); err != nil {
		return err
	}
	for decoder.More() {
		id, err := decoder.Token()
		if err != nil {
			return err
		}
		var trail json.RawMessage
		if err := decoder.Decode(&trail); err != nil {
			return err
		}
		if id == sessionID {
			encoder := json.NewEncoder(out)
			encoder.SetIndent("", "  ")
			return encoder.Encode(trail)
		}
	}
	return fmt.Errorf("session %q has no outcomes in this bundle", sessionID)
}
