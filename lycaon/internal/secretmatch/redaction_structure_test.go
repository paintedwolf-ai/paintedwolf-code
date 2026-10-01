package secretmatch

import (
	"context"
	"strings"
	"testing"
)

// harvestOf installs a fixed container-value source.
func harvestOf(t *testing.T, values ...HarvestedValue) *Matcher {
	t.Helper()
	m := loadBundled(t)
	for i := range values {
		values[i] = asContainerValue(values[i])
	}
	m.SetHarvestSource(func(context.Context) []HarvestedValue { return values })
	return m
}

func asContainerValue(v HarvestedValue) HarvestedValue {
	v.RuleID = HarvestRuleID
	v.Title = "A value from " + v.Container
	v.Source = SourceContainerHarvest
	return v
}

// Redaction preserves line breaks and numbering.
func TestRedactionNeverSpansALineBreak(t *testing.T) {
	in := "12\tkey: AAAAB3Nza\n13\tC1yc2EAAA\n14\tDAQABAAAB\n15\tnext: value"
	m := harvestOf(t, HarvestedValue{
		Name: "TLS_KEY", Container: ".kube/config",
		Secret: "AAAAB3Nza\nC1yc2EAAA\nDAQABAAAB",
	})
	out, spans := redactMatches(in, m.screenRaw(context.Background(), in))

	if len(spans) != 3 {
		t.Errorf("placeholders = %d, want 3 (one per covered line)", len(spans))
	}
	for _, keep := range []string{"12\t", "13\t", "14\t", "15\tnext: value"} {
		if !strings.Contains(out, keep) {
			t.Errorf("redaction dropped line structure %q from:\n%s", keep, out)
		}
	}
	if strings.Count(out, "\n") != strings.Count(in, "\n") {
		t.Errorf("line count changed: in=%d out=%d\n%s", strings.Count(in, "\n"), strings.Count(out, "\n"), out)
	}
	for _, gone := range []string{"AAAAB3Nza", "C1yc2EAAA", "DAQABAAAB"} {
		if strings.Contains(out, gone) {
			t.Errorf("redaction left secret line %q in:\n%s", gone, out)
		}
	}
}

// Line-numbered values match by individual lines.
func TestMultiLineContainerValueIsFoundThroughLineNumbering(t *testing.T) {
	m := harvestOf(t, HarvestedValue{
		Name: "PRIVATE_KEY", Container: "certs/server.pem",
		Secret: "MIIEowIBAAKC\nAQEAz9Kj4Lp2\nQhVn8sWtYbXc",
	})
	in := "1\t-----BEGIN RSA PRIVATE KEY-----\n2\tMIIEowIBAAKC\n3\tAQEAz9Kj4Lp2\n4\tQhVn8sWtYbXc\n5\t-----END RSA PRIVATE KEY-----"
	out, spans := redactMatches(in, m.screenRaw(context.Background(), in))
	if len(spans) == 0 {
		t.Fatalf("a multi-line key rendered with line numbers was not redacted at all:\n%s", out)
	}
	for _, gone := range []string{"MIIEowIBAAKC", "AQEAz9Kj4Lp2", "QhVn8sWtYbXc"} {
		if strings.Contains(out, gone) {
			t.Errorf("key line %q survived redaction in:\n%s", gone, out)
		}
	}
}

// Redaction preserves escaped line breaks in JSON tool bodies.
func TestRedactionKeepsJSONToolBodyIntact(t *testing.T) {
	m := harvestOf(t, HarvestedValue{
		Name: "DB_PASSWORD", Container: ".env", Secret: "s3cret-value-here",
	})
	in := `{"content":"38\t  password:\n39\t    s3cret-value-here\n40\t  port: 5432","total_lines":64}`
	out, _ := redactMatches(in, m.screenRaw(context.Background(), in))
	if strings.Contains(out, "s3cret-value-here") {
		t.Errorf("secret survived in JSON body:\n%s", out)
	}
	for _, keep := range []string{`\n39\t`, `\n40\t  port: 5432`, `"total_lines":64}`} {
		if !strings.Contains(out, keep) {
			t.Errorf("JSON body lost %q:\n%s", keep, out)
		}
	}
}

// Container values match only at token boundaries.
func TestContainerValueDoesNotMatchInsideALongerToken(t *testing.T) {
	m := harvestOf(t, HarvestedValue{
		Name: "MCP_TRANSPORT", Container: ".env", Secret: "streamable-http",
	})
	ctx := context.Background()
	glued := "var streamable-httpsuffix = 1"
	if out, spans := redactMatches(glued, m.screenRaw(ctx, glued)); len(spans) != 0 {
		t.Errorf("redacted inside a longer token: %s", out)
	}
	bare := "MCP_TRANSPORT=streamable-http"
	if out, spans := redactMatches(bare, m.screenRaw(ctx, bare)); len(spans) != 1 {
		t.Errorf("bare container value not redacted (n=%d): %s", len(spans), out)
	}
}

func TestShortContainerValuesAreNeverNeedles(t *testing.T) {
	for _, secret := range []string{"http", "str", "8765", "1.0.0"} {
		if got := harvestNeedles(secret, MinHarvestNeedleRunes); len(got) != 0 {
			t.Errorf("harvestNeedles(%q) = %v, want none — below the floor", secret, got)
		}
	}
}
