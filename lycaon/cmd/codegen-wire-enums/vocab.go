package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// EnumDef is one docs/openapi/vocab/<Name>.yaml file.
type EnumDef struct {
	Name          string        `yaml:"name"`
	Description   string        `yaml:"description"`
	GoConstPrefix string        `yaml:"go_const_prefix"`
	GoFile        string        `yaml:"go_file"`
	JSONSchema    string        `yaml:"json_schema"`
	HTTPStatus    bool          `yaml:"http_status"`
	TSLabels      *TSLabels     `yaml:"ts_labels"`
	StateMachine  *StateMachine `yaml:"state_machine"`
	Values        []EnumValue   `yaml:"values"`
	sourcePath    string
}

type StateMachine struct {
	Initial     []string            `yaml:"initial"`
	Transitions map[string][]string `yaml:"transitions"`
	SQLCheck    *SQLCheck           `yaml:"sql_check"`
	TSFile      string              `yaml:"ts_file"`
}

type SQLCheck struct {
	Path string `yaml:"path"`
}

// TSLabels supplies static enum labels absent from wire responses.
type TSLabels struct {
	Path       string `yaml:"path"`
	Const      string `yaml:"const"`
	TypeImport string `yaml:"type_import"`
	Doc        string `yaml:"doc"`
}

// EnumValue is one wire string.
type EnumValue struct {
	ID         string `yaml:"id"`
	Go         string `yaml:"go"`
	Comment    string `yaml:"comment"`
	Label      string `yaml:"label"`
	Payload    string `yaml:"payload"`
	Status     int    `yaml:"status"`
	Deprecated bool   `yaml:"deprecated"`
	Successor  string `yaml:"successor"`
}

func loadVocabDir(dir string) ([]EnumDef, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []EnumDef
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(dir, ent.Name())
		data, err := os.ReadFile(path) // #nosec G304 -- vocab dir from flag
		if err != nil {
			return nil, err
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		var e EnumDef
		if err := dec.Decode(&e); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		e.sourcePath = path
		if err := e.validate(ent.Name()); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (e *EnumDef) validate(fileName string) error {
	e.Name = strings.TrimSpace(e.Name)
	if e.Name == "" {
		return fmt.Errorf("name required")
	}
	want := e.Name + ".yaml"
	if fileName != want {
		return fmt.Errorf("filename must be %s", want)
	}
	if strings.TrimSpace(e.GoConstPrefix) == "" {
		e.GoConstPrefix = e.Name
	}
	if len(e.Values) == 0 {
		return fmt.Errorf("values required")
	}
	seen := map[string]struct{}{}
	for i := range e.Values {
		id := strings.TrimSpace(e.Values[i].ID)
		if id == "" {
			return fmt.Errorf("values[%d]: id required", i)
		}
		if _, dup := seen[id]; dup {
			return fmt.Errorf("duplicate id %q", id)
		}
		seen[id] = struct{}{}
		e.Values[i].ID = id
		e.Values[i].Go = strings.TrimSpace(e.Values[i].Go)
		e.Values[i].Comment = strings.TrimSpace(e.Values[i].Comment)
		e.Values[i].Label = strings.TrimSpace(e.Values[i].Label)
		e.Values[i].Payload = strings.TrimSpace(e.Values[i].Payload)
		e.Values[i].Successor = strings.TrimSpace(e.Values[i].Successor)
		if e.Name == "EventTopic" && e.Values[i].Payload == "" {
			return fmt.Errorf("values[%d]: EventTopic payload required", i)
		}
		if e.Name != "EventTopic" && e.Values[i].Payload != "" {
			return fmt.Errorf("values[%d]: payload is only valid for EventTopic", i)
		}
		if err := e.validateStatus(i); err != nil {
			return err
		}
	}
	for i, value := range e.Values {
		if value.Deprecated && value.Successor == "" {
			return fmt.Errorf("values[%d]: deprecated value requires successor", i)
		}
		if !value.Deprecated && value.Successor != "" {
			return fmt.Errorf("values[%d]: successor requires deprecated", i)
		}
		if value.Successor == value.ID {
			return fmt.Errorf("values[%d]: successor must differ from id", i)
		}
		if value.Successor != "" {
			if _, ok := seen[value.Successor]; !ok {
				return fmt.Errorf("values[%d]: successor %q is not in the vocabulary", i, value.Successor)
			}
		}
	}
	if err := e.validateTSLabels(); err != nil {
		return err
	}
	return e.validateStateMachine(seen)
}

// validateStatus requires one HTTP error status per value of an enum that
// declares http_status, and rejects statuses anywhere else.
func (e *EnumDef) validateStatus(i int) error {
	status := e.Values[i].Status
	if !e.HTTPStatus {
		if status != 0 {
			return fmt.Errorf("values[%d]: status is only valid for an enum that declares http_status", i)
		}
		return nil
	}
	if status < 400 || status > 599 {
		return fmt.Errorf("values[%d]: http_status enum needs an error status (400-599), got %d", i, status)
	}
	return nil
}

func (e *EnumDef) validateStateMachine(values map[string]struct{}) error {
	if e.StateMachine == nil {
		return nil
	}
	if len(e.StateMachine.Transitions) != len(values) {
		return fmt.Errorf("state_machine transitions must name every value")
	}
	if len(e.StateMachine.Initial) == 0 {
		return fmt.Errorf("state_machine initial values required")
	}
	for _, initial := range e.StateMachine.Initial {
		if _, ok := values[initial]; !ok {
			return fmt.Errorf("state_machine initial value %q is not a value", initial)
		}
	}
	for from, targets := range e.StateMachine.Transitions {
		if _, ok := values[from]; !ok {
			return fmt.Errorf("state_machine transition source %q is not a value", from)
		}
		for _, target := range targets {
			if _, ok := values[target]; !ok {
				return fmt.Errorf("state_machine transition target %q is not a value", target)
			}
		}
	}
	if strings.TrimSpace(e.JSONSchema) == "" || e.StateMachine.SQLCheck == nil || strings.TrimSpace(e.StateMachine.SQLCheck.Path) == "" || strings.TrimSpace(e.StateMachine.TSFile) == "" {
		return fmt.Errorf("state_machine needs json_schema, sql_check.path, and ts_file")
	}
	return nil
}

// validateTSLabels requires complete label tables.
func (e *EnumDef) validateTSLabels() error {
	t := e.TSLabels
	if t == nil {
		for _, v := range e.Values {
			if v.Label != "" {
				return fmt.Errorf("value %q has a label but the enum declares no ts_labels", v.ID)
			}
		}
		return nil
	}
	t.Path = strings.TrimSpace(t.Path)
	t.Const = strings.TrimSpace(t.Const)
	t.TypeImport = strings.TrimSpace(t.TypeImport)
	t.Doc = strings.TrimSpace(t.Doc)
	if t.Path == "" || t.Const == "" || t.TypeImport == "" {
		return fmt.Errorf("ts_labels needs path, const and type_import")
	}
	for _, v := range e.Values {
		if v.Label == "" {
			return fmt.Errorf("ts_labels declared but value %q has no label", v.ID)
		}
	}
	return nil
}

func (e EnumDef) openAPIFileName() string {
	return kebabSchemaFile(e.Name) + ".generated.yaml"
}

func (e EnumDef) goFileName() string {
	if f := strings.TrimSpace(e.GoFile); f != "" {
		return f
	}
	return snakeFile(e.Name) + "_ids.generated.go"
}

func (e EnumDef) constName(v EnumValue) (string, error) {
	suffix := v.Go
	if suffix == "" {
		suffix = snakeToExported(v.ID)
	}
	if suffix == "" {
		return "", fmt.Errorf("cannot derive const name for %s %q", e.Name, v.ID)
	}
	return e.GoConstPrefix + suffix, nil
}

func snakeFile(name string) string {
	var b strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
		}
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r - 'A' + 'a')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func snakeToExported(id string) string {
	parts := strings.Split(id, "_")
	var b strings.Builder
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
		if len(p) > 1 {
			b.WriteString(p[1:])
		}
	}
	return b.String()
}
