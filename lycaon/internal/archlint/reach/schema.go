package reach

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Schema lists the tables a fresh store creates and the view and trigger
// bodies that read tables inside the database.
type Schema struct {
	Tables  []Table
	Readers []string
}

// Table is one CREATE TABLE and where it is declared.
type Table struct {
	Name string
	Pos  string
}

var (
	createTable  = regexp.MustCompile(`(?i)^\s*create\s+(?:virtual\s+)?table\s+(?:if\s+not\s+exists\s+)?["` + "`" + `]?([a-z_][a-z0-9_]*)`)
	createReader = regexp.MustCompile(`(?i)^\s*create\s+(?:temp\s+|temporary\s+)?(?:view|trigger)\b`)
)

// ParseSchema reads a schema file. Each statement starts at a CREATE line; a
// view or trigger runs to the next CREATE.
func ParseSchema(path string) (Schema, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Schema{}, fmt.Errorf("schema: %w", err)
	}
	var s Schema
	var reader *strings.Builder
	for i, line := range strings.Split(string(raw), "\n") {
		if m := createTable.FindStringSubmatch(line); m != nil {
			s.flush(reader)
			reader = nil
			s.Tables = append(s.Tables, Table{Name: m[1], Pos: fmt.Sprintf("%s:%d", path, i+1)})
			continue
		}
		if createReader.MatchString(line) {
			s.flush(reader)
			reader = &strings.Builder{}
		} else if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "create ") {
			s.flush(reader)
			reader = nil
		}
		if reader != nil {
			reader.WriteString(line)
			reader.WriteByte('\n')
		}
	}
	s.flush(reader)
	return s, nil
}

func (s *Schema) flush(reader *strings.Builder) {
	if reader != nil {
		s.Readers = append(s.Readers, reader.String())
	}
}
