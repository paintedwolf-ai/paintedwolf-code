package detectionpack

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// Rule is one parsed Sigma rule. An unsupported rule is retained so the UI can
// show why it is inert; Matches always returns false for it.
type Rule struct {
	ID                string
	Title             string
	Description       string
	Level             Level
	Source            LogSource
	Status            string
	Author            string
	Date              string
	Modified          string
	References        []string
	Tags              []string
	Supported         bool
	UnsupportedReason string
	// Slug is the rules/<slug>.yml stem, set by the catalog loader for fixtures and Settings.
	Slug string

	selections map[string]selection
	condition  conditionNode
}

var allowedTopLevelKeys = map[string]struct{}{
	"title": {}, "id": {}, "status": {}, "description": {}, "references": {},
	"author": {}, "date": {}, "modified": {}, "logsource": {}, "detection": {}, "level": {},
	"tags": {}, "falsepositives": {},
}

var selectionNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var isoDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

type ruleDocument struct {
	raw       map[string]*yaml.Node
	detection *yaml.Node
	rule      Rule
}

// ParseRule parses one Sigma rule document. It returns an error only for input
// that is not a usable rule at all. Unsupported subset constructs yield
// Supported=false with UnsupportedReason and a nil error.
func ParseRule(data []byte) (Rule, error) {
	doc, err := decodeRuleDocument(data)
	if err != nil {
		return Rule{}, err
	}
	raw := doc.raw
	detNode := doc.detection
	r := doc.rule

	if n, ok := raw["status"]; ok {
		if s := strings.TrimSpace(nodeString(n)); s != "" {
			r.Status = s
		}
	}
	switch r.Status {
	case "stable", "test", "experimental":
	default:
		r.UnsupportedReason = fmt.Sprintf("unsupported status: %s", r.Status)
		return r, nil
	}
	if n, ok := raw["references"]; ok {
		r.References = nodeStringList(n)
	}
	if n, ok := raw["author"]; ok {
		r.Author = strings.TrimSpace(nodeString(n))
	}
	if n, ok := raw["date"]; ok {
		r.Date = strings.TrimSpace(nodeString(n))
	}
	if n, ok := raw["modified"]; ok {
		r.Modified = strings.TrimSpace(nodeString(n))
	}
	if n, ok := raw["tags"]; ok {
		r.Tags = nodeStringList(n)
	}
	if r.Status == "stable" {
		if r.Author == "" || len(r.References) == 0 || !isoDatePattern.MatchString(r.Date) || !isoDatePattern.MatchString(r.Modified) {
			r.UnsupportedReason = "stable rules require author, references, ISO date, and ISO modified"
			return r, nil
		}
	}

	// Unknown top-level keys ⇒ unsupported (still return the rule).
	for key := range raw {
		if _, ok := allowedTopLevelKeys[key]; !ok {
			r.UnsupportedReason = fmt.Sprintf("unknown key: %s", key)
			return r, nil
		}
	}

	lsNode, ok := raw["logsource"]
	if !ok || lsNode.Kind != yaml.MappingNode {
		r.UnsupportedReason = "unsupported logsource"
		return r, nil
	}
	product, service := "", ""
	for i := 0; i+1 < len(lsNode.Content); i += 2 {
		k := lsNode.Content[i].Value
		v := strings.TrimSpace(nodeString(lsNode.Content[i+1]))
		switch k {
		case "product":
			product = v
		case "service":
			service = v
		default:
			r.UnsupportedReason = fmt.Sprintf("unknown logsource key: %s", k)
			return r, nil
		}
	}
	if product != "lycaon" || (service != string(SourceToolExec) && service != string(SourceEgressObserved)) {
		r.UnsupportedReason = "unsupported logsource"
		return r, nil
	}
	r.Source = LogSource(service)
	allowedFields := fieldsForSource(r.Source)

	// Parse selections + condition from detection mapping.
	var conditionExpr string
	selectionCount := 0
	for i := 0; i+1 < len(detNode.Content); i += 2 {
		key := detNode.Content[i].Value
		val := detNode.Content[i+1]
		if key == "condition" {
			conditionExpr = strings.TrimSpace(nodeString(val))
			continue
		}
		if !selectionNamePattern.MatchString(key) {
			r.UnsupportedReason = fmt.Sprintf("invalid selection name: %s", key)
			return r, nil
		}
		if _, duplicate := r.selections[key]; duplicate {
			r.UnsupportedReason = fmt.Sprintf("duplicate selection name: %s", key)
			return r, nil
		}
		sel, err := compileSelection(val, allowedFields)
		if err != nil {
			r.UnsupportedReason = err.Error()
			return r, nil //nolint:nilerr // unsupported selection is catalog metadata, not a hard failure
		}
		r.selections[key] = sel
		selectionCount++
	}
	if selectionCount == 0 || conditionExpr == "" {
		return Rule{}, fmt.Errorf("detection requires ≥1 selection and a condition")
	}

	cond, err := parseCondition(conditionExpr, r.selections)
	if err != nil {
		r.UnsupportedReason = err.Error()
		return r, nil //nolint:nilerr // unsupported condition is catalog metadata, not a hard failure
	}
	r.condition = cond
	r.Supported = true
	return r, nil
}

func decodeRuleDocument(data []byte) (ruleDocument, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return ruleDocument{}, fmt.Errorf("unreadable YAML: %w", err)
	}
	doc := &root
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		doc = root.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return ruleDocument{}, fmt.Errorf("rule must be a YAML mapping")
	}

	raw := map[string]*yaml.Node{}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		key := doc.Content[i].Value
		raw[key] = doc.Content[i+1]
	}

	idNode, ok := raw["id"]
	if !ok || strings.TrimSpace(idNode.Value) == "" {
		return ruleDocument{}, fmt.Errorf("missing id")
	}
	id := strings.TrimSpace(idNode.Value)
	if _, err := uuid.Parse(id); err != nil {
		return ruleDocument{}, fmt.Errorf("id must be an RFC 4122 UUID")
	}

	titleNode, ok := raw["title"]
	if !ok || strings.TrimSpace(nodeString(titleNode)) == "" {
		return ruleDocument{}, fmt.Errorf("missing title")
	}
	title := strings.TrimSpace(nodeString(titleNode))
	if len(title) > 80 {
		return ruleDocument{}, fmt.Errorf("title exceeds 80 characters")
	}
	descriptionNode, ok := raw["description"]
	if !ok || strings.TrimSpace(nodeString(descriptionNode)) == "" {
		return ruleDocument{}, fmt.Errorf("missing description")
	}
	description := strings.TrimSpace(nodeString(descriptionNode))

	levelNode, ok := raw["level"]
	if !ok || strings.TrimSpace(nodeString(levelNode)) == "" {
		return ruleDocument{}, fmt.Errorf("missing level")
	}
	levelStr := strings.ToLower(strings.TrimSpace(nodeString(levelNode)))
	level := Level(levelStr)
	switch level {
	case LevelInformational, LevelLow, LevelMedium, LevelHigh, LevelCritical:
	default:
		return ruleDocument{}, fmt.Errorf("invalid level %q", levelStr)
	}

	detNode, ok := raw["detection"]
	if !ok || detNode == nil || detNode.Kind != yaml.MappingNode {
		return ruleDocument{}, fmt.Errorf("missing detection")
	}

	r := Rule{
		ID:          id,
		Title:       title,
		Level:       level,
		Description: description,
		Status:      "experimental",
		selections:  map[string]selection{},
	}
	return ruleDocument{raw: raw, detection: detNode, rule: r}, nil
}

func compileSelection(node *yaml.Node, allowedFields map[string]struct{}) (selection, error) {
	if node.Kind != yaml.MappingNode {
		return selection{}, fmt.Errorf("selection must be a mapping")
	}
	var matchers []fieldMatcher
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		raw, err := yamlNodeToAny(node.Content[i+1])
		if err != nil {
			return selection{}, err
		}
		fm, err := compileFieldMatcher(key, raw, allowedFields)
		if err != nil {
			return selection{}, err
		}
		matchers = append(matchers, fm)
	}
	if len(matchers) == 0 {
		return selection{}, fmt.Errorf("selection must have at least one field")
	}
	return selection{matchers: matchers}, nil
}

func yamlNodeToAny(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag != "!!str" {
			return nil, fmt.Errorf("field values must be strings")
		}
		return n.Value, nil
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode || c.Tag != "!!str" {
				return nil, fmt.Errorf("field values must be strings")
			}
			out = append(out, c.Value)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("field values must be strings")
	}
}

func nodeString(n *yaml.Node) string {
	if n == nil {
		return ""
	}
	if n.Kind == yaml.ScalarNode {
		return n.Value
	}
	return ""
}

func nodeStringList(n *yaml.Node) []string {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.ScalarNode {
		if n.Value == "" {
			return nil
		}
		return []string{n.Value}
	}
	if n.Kind != yaml.SequenceNode {
		return nil
	}
	out := make([]string, 0, len(n.Content))
	for _, c := range n.Content {
		if c.Kind == yaml.ScalarNode {
			out = append(out, c.Value)
		}
	}
	return out
}

// FieldLiterals returns the literal values this rule matches against one event field,
// across every selection, deduplicated and sorted.
//
// It lets a boundary read a pack's paths as data rather than from a detection
// verdict: confine answers "is this a credential store" from the catalogue, not
// from whether a rule fired. Regex matchers are excluded, since a pattern is not
// a path.
func (r Rule) FieldLiterals(field string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, sel := range r.selections {
		for _, m := range sel.matchers {
			if m.field != field || m.op == opRegex {
				continue
			}
			for _, value := range m.raw {
				value = strings.TrimSpace(value)
				if value == "" {
					continue
				}
				if _, dup := seen[value]; dup {
					continue
				}
				seen[value] = struct{}{}
				out = append(out, value)
			}
		}
	}
	sort.Strings(out)
	return out
}
