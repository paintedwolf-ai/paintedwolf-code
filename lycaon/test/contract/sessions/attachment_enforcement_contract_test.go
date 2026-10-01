package contract

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Prompt envelopes carry handles, not payloads or locators.
func TestPromptEnvelopeIntakeBoundaries(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	internal := filepath.Join(root, "lycaon", "internal")
	for _, envelope := range []struct {
		typeOf    reflect.Type
		forbidden []string
	}{
		{reflect.TypeFor[api.PromptAttachmentPart](), []string{"url", "path", "file_path", "href", "bytes", "content"}},
		{reflect.TypeFor[api.PromptReferencePart](), []string{"url", "bytes", "content"}},
	} {
		for _, field := range reflect.VisibleFields(envelope.typeOf) {
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if field.PkgPath != "" || name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			for _, forbidden := range envelope.forbidden {
				if name == forbidden {
					t.Errorf("%s carries forbidden JSON field %q", envelope.typeOf.Name(), name)
				}
			}
			if field.Type.Kind() == reflect.Slice && field.Type.Elem().Kind() == reflect.Uint8 {
				t.Errorf("%s.%s carries inline bytes", envelope.typeOf.Name(), field.Name)
			}
		}
	}

	// Intake must not open caller-supplied filesystem paths for attachments.
	forbiddenInternal := []string{
		`http\.Get\(`,
		`os\.Open\(part\.`,
		`os\.ReadFile\(part\.`,
		`ioutil\.ReadFile\(part\.`,
		`filepath\.Walk\(part\.`,
	}
	attachPaths := []string{
		filepath.Join(internal, "promptattach"),
		filepath.Join(internal, "api", "promptadmin", "prompt_attachments.go"),
	}
	attachSource := readContractSources(t, attachPaths...)
	for _, pattern := range forbiddenInternal {
		if regexp.MustCompile(pattern).MatchString(attachSource) {
			t.Fatalf("forbidden attach ingest pattern %q matched", pattern)
		}
	}

	apiSource := readContractSources(t, filepath.Join(internal, "api"))
	if !strings.Contains(apiSource, "IngestAttachments") {
		t.Fatal("expected prompt API to call IngestAttachments")
	}
	if !strings.Contains(apiSource, "IngestReferences") {
		t.Fatal("expected prompt API to call IngestReferences")
	}

	// Reference intake must not open caller-supplied filesystem paths for content.
	refForbidden := []string{
		`os\.Open\(`,
		`os\.ReadFile\(`,
		`ioutil\.ReadFile\(`,
		`filepath\.Walk\(`,
	}
	refPaths := []string{
		filepath.Join(internal, "promptattach", "references.go"),
		filepath.Join(internal, "api", "promptadmin", "prompt_references.go"),
	}
	refSource := readContractSources(t, refPaths...)
	for _, pattern := range refForbidden {
		if regexp.MustCompile(pattern).MatchString(refSource) {
			t.Fatalf("forbidden reference ingest pattern %q matched", pattern)
		}
	}
}

func TestAttachmentCapsLoaderPresent(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	budgets := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "host", "prompt-budgets.yaml")
	raw, err := os.ReadFile(budgets)
	testutil.FailErr(t, "read prompt budgets", err)
	if !regexp.MustCompile(`(?m)^prompt_attachments:`).Match(raw) {
		t.Fatal("prompt-budgets.yaml missing live prompt_attachments: block")
	}
}

func readContractSources(t *testing.T, paths ...string) string {
	t.Helper()
	var out strings.Builder
	for _, root := range paths {
		info, err := os.Stat(root)
		testutil.FailErr(t, "stat contract source", err)
		if !info.IsDir() {
			raw, readErr := os.ReadFile(root)
			testutil.FailErr(t, "read contract source", readErr)
			out.Write(raw)
			continue
		}
		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") {
				return walkErr
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			out.Write(raw)
			return nil
		})
		testutil.FailErr(t, "walk contract sources", err)
	}
	return out.String()
}
