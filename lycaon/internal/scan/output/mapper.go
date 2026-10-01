package output

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/pkg/api"
	"gopkg.in/yaml.v3"
)

// Device mappers: declarative JSON-pointer field maps for the
// map/json output parser. Mappers live only on the device under
// {configdir}/scanners/mappers/<mapper_id>.yaml — never in packs or projects.

var mapperIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// IsValidMapperID reports whether id matches the locked mapper id shape
// (lowercase, no path separators).
func IsValidMapperID(id string) bool {
	return mapperIDPattern.MatchString(id)
}

// MapperPath returns {configDir}/scanners/mappers/<mapperID>.yaml.
func MapperPath(configDir, mapperID string) string {
	return filepath.Join(configDir, "scanners", "mappers", mapperID+".yaml")
}

// Mapper is the device JSON mapper schema.
type Mapper struct {
	ID           string            `yaml:"id"`
	StdoutFormat string            `yaml:"stdout_format"`
	ItemsPath    string            `yaml:"items_path"`
	Fields       MapperFields      `yaml:"fields"`
	LevelMap     map[string]string `yaml:"level_map,omitempty"`
}

// MapperFields are JSON pointers relative to each item.
type MapperFields struct {
	RuleID    string `yaml:"rule_id"`
	Level     string `yaml:"level"`
	Message   string `yaml:"message,omitempty"`
	URI       string `yaml:"uri,omitempty"`
	StartLine string `yaml:"start_line,omitempty"`
}

// LoadMapper reads and validates the device mapper body for mapperID.
func LoadMapper(configDir, mapperID string) (*Mapper, error) {
	mapperID = strings.TrimSpace(mapperID)
	if mapperID == "" {
		return nil, fmt.Errorf("mapper id is required")
	}
	if !IsValidMapperID(mapperID) {
		return nil, fmt.Errorf("mapper id %q must match %s", mapperID, mapperIDPattern.String())
	}
	if strings.TrimSpace(configDir) == "" {
		return nil, fmt.Errorf("config dir required for mapper %q", mapperID)
	}
	data, err := os.ReadFile(MapperPath(configDir, mapperID))
	if err != nil {
		return nil, fmt.Errorf("mapper %q: %w", mapperID, err)
	}
	var m Mapper
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("mapper %q: %w", mapperID, err)
	}
	if strings.TrimSpace(m.ID) != mapperID {
		return nil, fmt.Errorf("mapper %q: id %q must equal filename stem", mapperID, m.ID)
	}
	if strings.TrimSpace(m.StdoutFormat) != "json" {
		return nil, fmt.Errorf("mapper %q: stdout_format must be json (got %q)", mapperID, m.StdoutFormat)
	}
	if strings.TrimSpace(m.ItemsPath) == "" {
		return nil, fmt.Errorf("mapper %q: items_path is required", mapperID)
	}
	if strings.TrimSpace(m.Fields.RuleID) == "" {
		return nil, fmt.Errorf("mapper %q: fields.rule_id is required", mapperID)
	}
	if strings.TrimSpace(m.Fields.Level) == "" {
		return nil, fmt.Errorf("mapper %q: fields.level is required", mapperID)
	}
	for from, to := range m.LevelMap {
		if _, ok := findingsJSONLevel(to); !ok {
			return nil, fmt.Errorf("mapper %q: level_map[%q]=%q does not map to a finding level", mapperID, from, to)
		}
	}
	return &m, nil
}

// NewMapJSONParser builds the map/json output parser bound to a loaded mapper.
func NewMapJSONParser(m *Mapper) OutputParser {
	return mapJSONParser{mapper: m}
}

type mapJSONParser struct {
	mapper *Mapper
}

func (mapJSONParser) ID() string { return OutputParserMapJSON }

func (p mapJSONParser) Parse(raw []byte) (*Result, error) {
	if p.mapper == nil {
		return nil, fmt.Errorf("map/json: device mapper required")
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("map/json: empty output")
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("map/json: invalid JSON (%d bytes): %w", len(raw), err)
	}
	itemsRaw, ok := evalJSONPointer(doc, p.mapper.ItemsPath)
	if !ok {
		return nil, fmt.Errorf("map/json: items_path %q not found", p.mapper.ItemsPath)
	}
	items, ok := itemsRaw.([]any)
	if !ok {
		return nil, fmt.Errorf("map/json: items_path %q is not an array", p.mapper.ItemsPath)
	}
	result := &Result{
		Categories: []api.ScanCategory{api.ScanCategorySecurity},
		Findings:   make([]api.SecurityFinding, 0, len(items)),
	}
	for _, item := range items {
		if finding, ok := p.mapItem(item); ok {
			result.Findings = append(result.Findings, finding)
		}
	}
	result.FindingsCount = len(result.Findings)
	return result, nil
}

// mapItem projects one row and skips rows without a rule id.
func (p mapJSONParser) mapItem(item any) (api.SecurityFinding, bool) {
	ruleID := strings.TrimSpace(pointerString(item, p.mapper.Fields.RuleID))
	if ruleID == "" {
		return api.SecurityFinding{}, false
	}
	levelRaw := strings.TrimSpace(pointerString(item, p.mapper.Fields.Level))
	if mapped, ok := p.mapper.LevelMap[levelRaw]; ok {
		levelRaw = mapped
	}
	loc := api.SecurityFindingLocation{}
	if p.mapper.Fields.URI != "" {
		loc.URI = strings.TrimSpace(pointerString(item, p.mapper.Fields.URI))
	}
	if p.mapper.Fields.StartLine != "" {
		if line, ok := pointerInt(item, p.mapper.Fields.StartLine); ok && line > 0 {
			loc.StartLine = line
		}
	}
	message := ""
	if p.mapper.Fields.Message != "" {
		message = pointerString(item, p.mapper.Fields.Message)
	}
	return scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
		DriverID:   p.mapper.ID,
		RuleID:     ruleID,
		Level:      normalizeFindingsJSONLevel(levelRaw),
		Message:    message,
		Kind:       api.FindingKindCustom,
		Locations:  []api.SecurityFindingLocation{loc},
		Categories: []api.ScanCategory{api.ScanCategorySecurity},
	}), true
}

func pointerString(doc any, pointer string) string {
	v, ok := evalJSONPointer(doc, pointer)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

func pointerInt(doc any, pointer string) (int, bool) {
	v, ok := evalJSONPointer(doc, pointer)
	if !ok {
		return 0, false
	}
	f, ok := v.(float64)
	if !ok {
		return 0, false
	}
	return int(f), true
}

// evalJSONPointer walks an RFC 6901 pointer over decoded JSON. No scripts,
// filters, or wildcards — declarative pointers only.
func evalJSONPointer(doc any, pointer string) (any, bool) {
	if pointer == "" {
		return doc, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	current := doc
	for _, segment := range strings.Split(pointer[1:], "/") {
		segment = strings.ReplaceAll(segment, "~1", "/")
		segment = strings.ReplaceAll(segment, "~0", "~")
		switch node := current.(type) {
		case map[string]any:
			next, ok := node[segment]
			if !ok {
				return nil, false
			}
			current = next
		case []any:
			idx, ok := arrayIndex(segment, len(node))
			if !ok {
				return nil, false
			}
			current = node[idx]
		default:
			return nil, false
		}
	}
	return current, true
}

func arrayIndex(segment string, length int) (int, bool) {
	if segment == "" || (len(segment) > 1 && segment[0] == '0') {
		return 0, false
	}
	idx := 0
	for _, r := range segment {
		if r < '0' || r > '9' {
			return 0, false
		}
		idx = idx*10 + int(r-'0')
		// Stopping at the bound also keeps long segments from overflowing idx.
		if idx >= length {
			return 0, false
		}
	}
	return idx, true
}
