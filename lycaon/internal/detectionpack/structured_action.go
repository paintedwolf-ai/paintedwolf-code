package detectionpack

import (
	"strings"
	"unicode"
)

// structuredActionArgs names fields that carry structured operations.
var structuredActionArgs = map[string]struct{}{
	"action": {}, "api_action": {}, "operation": {}, "operation_name": {},
}

// iamServiceCLINames maps service prefixes to command-service spellings.
var iamServiceCLINames = map[string][]string{
	"s3":                   {"s3api", "s3control"},
	"elasticmapreduce":     {"emr"},
	"elasticloadbalancing": {"elbv2", "elb"},
	"tag":                  {"resourcegroupstaggingapi"},
	"ses":                  {"ses", "sesv2"},
}

// APIActionsFromStructuredArgs returns normalized operation identities.
func APIActionsFromStructuredArgs(args map[string]any) []string {
	var out []string
	for name, value := range args {
		if _, ok := structuredActionArgs[strings.ToLower(strings.TrimSpace(name))]; !ok {
			continue
		}
		raw, ok := value.(string)
		if !ok {
			continue
		}
		out = append(out, apiActionsFromIdentity(raw)...)
	}
	return canonicalBounded(out, 64)
}

// apiActionsFromIdentity converts one service operation to command spellings.
func apiActionsFromIdentity(identity string) []string {
	identity = strings.TrimSpace(identity)
	prefix, operation, found := strings.Cut(identity, ":")
	if !found || prefix == "" || operation == "" {
		return nil
	}
	// Dotted and slashed identities name other providers' operations.
	if strings.ContainsAny(identity, " /.") {
		return nil
	}
	verb := kebabOperation(operation)
	if verb == "" {
		return nil
	}
	services, ok := iamServiceCLINames[strings.ToLower(prefix)]
	if !ok {
		services = []string{strings.ToLower(prefix)}
	}
	out := make([]string, 0, len(services))
	for _, service := range services {
		out = append(out, service+":"+verb)
	}
	return out
}

// kebabOperation preserves acronym runs while inserting word separators.
func kebabOperation(operation string) string {
	runes := []rune(operation)
	var b strings.Builder
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return ""
		}
		if i > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			next := rune(0)
			if i+1 < len(runes) {
				next = runes[i+1]
			}
			// Split at case transitions and acronym endings.
			if !unicode.IsUpper(prev) || (next != 0 && unicode.IsLower(next)) {
				b.WriteByte('-')
			}
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}
