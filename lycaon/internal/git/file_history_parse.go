package git

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// NUL delimiters preserve control characters in filenames.
func parseFileHistory(out string) ([]GitFileCommit, error) {
	var commits []GitFileCommit
	tokens := strings.Split(out, "\x00")
	for index := 0; index < len(tokens); {
		if tokens[index] == "" || tokens[index] == "\n" {
			index++
			continue
		}
		if len(tokens)-index < 5 || !validObjectID(tokens[index]) {
			return nil, fmt.Errorf("invalid file history header")
		}
		authored, err := strconv.ParseInt(tokens[index+2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("file history author date: %w", err)
		}
		committed, err := strconv.ParseInt(tokens[index+3], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("file history commit date: %w", err)
		}
		// Copies release the subprocess output buffer.
		row := GitFileCommit{Hash: strings.Clone(tokens[index]), AuthorName: strings.Clone(tokens[index+1]), Subject: strings.Clone(tokens[index+4]),
			AuthoredAt: time.Unix(authored, 0).UTC(), CommittedAt: time.Unix(committed, 0).UTC()}
		index += 5
		for index < len(tokens) && (tokens[index] == "" || tokens[index] == "\n") {
			index++
		}
		if index >= len(tokens) || validObjectID(tokens[index]) {
			continue
		}
		fields := strings.Fields(tokens[index])
		if len(fields) != 5 || !strings.HasPrefix(fields[0], ":") ||
			!validObjectID(fields[2]) || !validObjectID(fields[3]) ||
			index+1 >= len(tokens) || tokens[index+1] == "" {
			return nil, fmt.Errorf("invalid file history change")
		}
		row.Path, row.BlobOID = strings.Clone(tokens[index+1]), strings.Clone(fields[3])
		if strings.Trim(row.BlobOID, "0") == "" {
			row.BlobOID = ""
		}
		commits = append(commits, row)
		index += 2
	}
	return commits, nil
}
