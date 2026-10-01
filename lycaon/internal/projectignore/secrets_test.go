package projectignore

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"gopkg.in/yaml.v3"
)

func fixture(t *testing.T) (*SecretService, Root) {
	t.Helper()
	root := Root{ID: "root", Path: t.TempDir()}
	service := &SecretService{
		Roots: func(_ context.Context, p string) ([]Root, error) {
			if p != "project" {
				return nil, nil
			}
			return []Root{root}, nil
		},
	}
	return service, root
}

func appendSecret(root string, entry SecretEntry) error {
	return Edit(root, "secrets", func(n *yaml.Node) error {
		var node yaml.Node
		if err := node.Encode(entry); err != nil {
			return err
		}
		n.Content = append(n.Content, &node)
		return nil
	})
}

func TestSecretDeclarationLifecycle(t *testing.T) {
	s, r := fixture(t)
	ctx := t.Context()
	testutil.FailErr(t, "write declaration", appendSecret(r.Path, SecretEntry{Value: "  fixture-token  ", Reason: "example"}))
	values := s.ActiveValues(ctx, "project")
	if len(values) != 1 || values[0].Value != "  fixture-token  " {
		t.Fatalf("unexpected values: %+v", values)
	}
	if len(s.ActiveValues(ctx, "other")) != 0 || len(s.ActiveValues(ctx, "")) != 0 {
		t.Fatal("exception crossed project boundary")
	}
	testutil.FailErr(t, "edit declaration", Edit(r.Path, "secrets", func(n *yaml.Node) error {
		return n.Content[0].Encode(SecretEntry{Value: "changed", Reason: "updated example"})
	}))
	values = s.ActiveValues(ctx, "project")
	if len(values) != 1 || values[0].Value != "changed" {
		t.Fatal("file edit did not replace active value")
	}
	s.Protected = func(context.Context, string, string) bool { return true }
	if len(s.ActiveValues(ctx, "project")) != 0 {
		t.Fatal("protected evidence did not override exception")
	}
	s.Protected = nil
	s.Roots = func(context.Context, string) ([]Root, error) { return nil, ErrUntrusted }
	if len(s.ActiveValues(ctx, "project")) != 0 {
		t.Fatal("untrusted project applied exception")
	}
	s.Roots = func(context.Context, string) ([]Root, error) { return []Root{r}, nil }
	testutil.FailErr(t, "remove declaration", Edit(r.Path, "secrets", func(n *yaml.Node) error { n.Content = nil; return nil }))
	if len(s.ActiveValues(ctx, "project")) != 0 {
		t.Fatal("removed exception remains active")
	}
}

func TestSharedDocumentPreservesSectionsAndComments(t *testing.T) {
	_, r := fixture(t)
	testutil.FailErr(t, "marker", settingsoverlay.EnsureCurrentFormat(r.Path))
	body := "# shared decisions\nversion: 1\nfindings:\n  - path: fixtures/** # keep me\n    reason: fixtures\nsecrets: []\n"
	testutil.FailErr(t, "fixture", os.WriteFile(Path(r.Path), []byte(body), 0o600))
	testutil.FailErr(t, "save value", appendSecret(r.Path, SecretEntry{Value: "fixture", Reason: "not a credential"}))
	data, err := Read(r.Path)
	testutil.FailErr(t, "read", err)
	for _, want := range []string{"# shared decisions", "path: fixtures/** # keep me", "value: fixture"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing preserved content %q", want)
		}
	}
	var wg sync.WaitGroup
	failures := make(chan error, 20)
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			failures <- Edit(r.Path, "findings", func(n *yaml.Node) error {
				var e yaml.Node
				err := e.Encode(map[string]string{"path": "tests/**", "reason": "fixture"})
				n.Content = append(n.Content, &e)
				return err
			})
		}()
		go func() {
			defer wg.Done()
			failures <- appendSecret(r.Path, SecretEntry{Value: "another fixture", Reason: "example"})
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		testutil.FailErr(t, "concurrent edit", err)
	}
	data, err = Read(r.Path)
	testutil.FailErr(t, "read concurrent", err)
	doc, err := Parse(data)
	testutil.FailErr(t, "parse", err)
	findings, err := Section(doc, "findings")
	testutil.FailErr(t, "findings", err)
	secrets, err := Section(doc, "secrets")
	testutil.FailErr(t, "secrets", err)
	if len(findings.Content) != 11 || len(secrets.Content) != 11 {
		t.Fatalf("lost updates: findings=%d secrets=%d", len(findings.Content), len(secrets.Content))
	}
}

func TestMalformedEntriesAreIsolatedAndExpiryIsExclusive(t *testing.T) {
	_, r := fixture(t)
	testutil.FailErr(t, "marker", settingsoverlay.EnsureCurrentFormat(r.Path))
	body := "version: 1\nfindings: wrong\nsecrets:\n  - value: valid\n    reason: fixture\n  - value: invalid\n    pattern: '.*'\n    reason: fixture\n"
	testutil.FailErr(t, "fixture", os.WriteFile(Path(r.Path), []byte(body), 0o600))
	entries, defects, err := ReadSecrets(r.Path)
	testutil.FailErr(t, "read secrets independently", err)
	if len(entries) != 1 || len(defects) != 1 {
		t.Fatalf("entries=%d defects=%d", len(entries), len(defects))
	}
	e := SecretEntry{Value: "test", Reason: "fixture", Expires: "2027-01-01"}
	if !e.Expired(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) || e.Expired(time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)) {
		t.Fatal("expiry must be exclusive UTC midnight")
	}
}

func TestParseRefusesUnknownEnvelopeAndMultipleDocuments(t *testing.T) {
	for _, body := range []string{"", " \n", "secrets: []\n", "version: 0\n", "version: 1.0\n", "version: 9\n", "version: 1\nignore: []\n", "version: 1\n---\nsecrets: []\n", "version: 1\nversion: 1\n"} {
		if _, err := Parse([]byte(body)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted unsupported document: %v", err)
		}
	}
}

func TestExternalConflictPreservesFileEdit(t *testing.T) {
	_, r := fixture(t)
	testutil.FailErr(t, "write declaration", appendSecret(r.Path, SecretEntry{Value: "fixture", Reason: "example"}))
	original, err := Read(r.Path)
	testutil.FailErr(t, "read original", err)
	concurrent := append([]byte("# external edit\n"), original...)
	err = Edit(r.Path, "secrets", func(n *yaml.Node) error {
		n.Content = nil
		return os.WriteFile(Path(r.Path), concurrent, 0o600)
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("concurrent save: %v", err)
	}
	after, err := Read(r.Path)
	testutil.FailErr(t, "read after conflict", err)
	if string(after) != string(concurrent) {
		t.Fatal("overwrote external edit")
	}
	if SectionDigest(original, "findings") != SectionDigest(after, "findings") {
		t.Fatal("secret edit invalidated scanner rule digest")
	}
}

func TestActiveValuesExcludeExpiredAndMalformedDeclarations(t *testing.T) {
	s, r := fixture(t)
	testutil.FailErr(t, "marker", settingsoverlay.EnsureCurrentFormat(r.Path))
	body := "version: 1\nsecrets:\n  - value: valid\n    reason: public fixture\n  - value: expired\n    reason: old fixture\n    expires: 2000-01-01\n  - value: invalid\n    pattern: .*\n    reason: fixture\n"
	testutil.FailErr(t, "write declarations", os.WriteFile(Path(r.Path), []byte(body), 0o600))
	values := s.ActiveValues(t.Context(), "project")
	if len(values) != 1 || values[0].Value != "valid" {
		t.Fatalf("unexpected active declarations: %+v", values)
	}
	testutil.FailErr(t, "remove file", os.Remove(Path(r.Path)))
	if len(s.ActiveValues(t.Context(), "project")) != 0 {
		t.Fatal("deleted file retained its exception")
	}
}

func TestSecretIgnoreAddPreservesFindingsAndRetries(t *testing.T) {
	s, root := fixture(t)
	entry := SecretEntry{ID: "fa40d86b-836e-4006-b74b-63c88c889eef", Value: "  public fixture\n", Reason: "documented example"}
	testutil.FailErr(t, "initial finding", Edit(root.Path, "findings", func(n *yaml.Node) error {
		var item yaml.Node
		if err := item.Encode(map[string]string{"path": "fixtures/**", "reason": "test fixtures"}); err != nil {
			return err
		}
		n.Content = append(n.Content, &item)
		return nil
	}))
	before, err := Read(root.Path)
	testutil.FailErr(t, "read initial document", err)
	testutil.FailErr(t, "add public value", s.Add(t.Context(), "project", root.ID, entry))
	testutil.FailErr(t, "retry same declaration", s.Add(t.Context(), "project", root.ID, entry))
	after, err := Read(root.Path)
	testutil.FailErr(t, "read resulting document", err)
	if SectionDigest(before, "findings") != SectionDigest(after, "findings") {
		t.Fatal("adding a secret ignore changed finding rules")
	}
	entries, _, err := ReadSecrets(root.Path)
	testutil.FailErr(t, "read entries", err)
	if len(entries) != 1 || entries[0] != entry {
		t.Fatalf("retry changed exact declaration: %+v", entries)
	}
	entry.Reason = "changed"
	if err := s.Add(t.Context(), "project", root.ID, entry); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting retry = %v", err)
	}
	s.Protected = func(context.Context, string, string) bool { return true }
	if err := s.Add(t.Context(), "project", root.ID, entry); !errors.Is(err, ErrProtected) {
		t.Fatalf("protected declaration = %v", err)
	}
	s.Protected = nil
	s.Trusted = func(context.Context, string) bool { return false }
	if len(s.ActiveValues(t.Context(), "project")) != 0 {
		t.Fatal("untrusted declaration applied")
	}
	if err := s.Add(t.Context(), "project", root.ID, entry); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("untrusted write = %v", err)
	}
}

func TestSecretReviewIsTransientAndProjectBound(t *testing.T) {
	var reviews SecretReviews
	ctx, cancel := context.WithCancel(t.Context())
	id, release := reviews.Offer(ctx, "project", "public fixture")
	value, err := reviews.Value("project", id)
	testutil.FailErr(t, "read candidate", err)
	if value != "public fixture" {
		t.Fatal("review changed exact bytes")
	}
	if _, err := reviews.Value("other", id); !errors.Is(err, ErrReviewUnavailable) {
		t.Fatalf("cross-project review = %v", err)
	}
	cancel()
	if _, err := reviews.Value("project", id); !errors.Is(err, ErrReviewUnavailable) {
		t.Fatalf("canceled review = %v", err)
	}
	release()
	id, release = reviews.Offer(t.Context(), "project", "public fixture")
	release()
	if _, err := reviews.Value("project", id); !errors.Is(err, ErrReviewUnavailable) {
		t.Fatalf("released review = %v", err)
	}
}

func TestUnsupportedDocumentIsNotRewritten(t *testing.T) {
	_, root := fixture(t)
	testutil.FailErr(t, "write format marker", settingsoverlay.EnsureCurrentFormat(root.Path))
	for _, body := range []string{"", "version: 0\nsecrets: []\n", "secrets: []\n"} {
		testutil.FailErr(t, "write unsupported document", os.WriteFile(Path(root.Path), []byte(body), 0o600))
		err := appendSecret(root.Path, SecretEntry{Value: "public fixture", Reason: "example"})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("unsupported document mutation: %v", err)
		}
		after, err := os.ReadFile(Path(root.Path))
		testutil.FailErr(t, "read unchanged document", err)
		if string(after) != body {
			t.Fatal("unsupported document was modified")
		}
	}
}

func TestSecretIgnoreRetryRejectsInvalidExistingDeclaration(t *testing.T) {
	for _, invalid := range []string{"    unexpected: true\n", "  - id: fa40d86b-836e-4006-b74b-63c88c889eef\n    value: public fixture\n    reason: example\n"} {
		s, root := fixture(t)
		testutil.FailErr(t, "write format marker", settingsoverlay.EnsureCurrentFormat(root.Path))
		body := "version: 1\nsecrets:\n  - id: fa40d86b-836e-4006-b74b-63c88c889eef\n    value: public fixture\n    reason: example\n" + invalid
		testutil.FailErr(t, "write invalid declaration", os.WriteFile(Path(root.Path), []byte(body), 0o600))
		err := s.Add(t.Context(), "project", root.ID, SecretEntry{ID: "fa40d86b-836e-4006-b74b-63c88c889eef", Value: "public fixture", Reason: "example"})
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("invalid retry = %v", err)
		}
		after, err := os.ReadFile(Path(root.Path))
		testutil.FailErr(t, "read unchanged declaration", err)
		if string(after) != body {
			t.Fatal("retry changed invalid declarations")
		}
	}
}

func TestSecretIgnoreRemoveWithdrawsExactlyOneDeclaration(t *testing.T) {
	s, root := fixture(t)
	testutil.FailErr(t, "write format marker", settingsoverlay.EnsureCurrentFormat(root.Path))
	body := "# shared decisions\nversion: 1\nfindings:\n  - path: fixtures/** # keep me\n    reason: fixtures\n" +
		"secrets:\n  - id: fa40d86b-836e-4006-b74b-63c88c889eef\n    value: identified\n    reason: labelled example\n" +
		"  - value: unlabelled\n    reason: anonymous example\n  - value: kept\n    pattern: '.*'\n    reason: refused example\n"
	testutil.FailErr(t, "write declarations", os.WriteFile(Path(root.Path), []byte(body), 0o600))

	catalog, err := s.List(t.Context(), "project")
	testutil.FailErr(t, "list declarations", err)
	if len(catalog.Rules) != 2 || len(catalog.Invalid) != 1 {
		t.Fatalf("rules=%d invalid=%d", len(catalog.Rules), len(catalog.Invalid))
	}
	if err := s.Remove(t.Context(), "project", root.ID, "entry:absent"); !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("unknown key = %v", err)
	}
	if err := s.Remove(t.Context(), "project", "other-root", catalog.Rules[0].Key()); !errors.Is(err, ErrRootNotFound) {
		t.Fatalf("unknown folder = %v", err)
	}
	unchanged, err := os.ReadFile(Path(root.Path))
	testutil.FailErr(t, "read refused removals", err)
	if string(unchanged) != body {
		t.Fatal("a refused removal changed the file")
	}

	before, err := Read(root.Path)
	testutil.FailErr(t, "read document", err)
	// Withdrawal narrows what the project ignores, so trust does not gate it.
	s.Trusted = func(context.Context, string) bool { return false }
	testutil.FailErr(t, "withdraw labelled declaration", s.Remove(t.Context(), "project", root.ID, catalog.Rules[0].Key()))
	testutil.FailErr(t, "withdraw unlabelled declaration", s.Remove(t.Context(), "project", root.ID, catalog.Rules[1].Key()))
	after, err := Read(root.Path)
	testutil.FailErr(t, "read resulting document", err)
	if SectionDigest(before, "findings") != SectionDigest(after, "findings") {
		t.Fatal("withdrawing a secret ignore changed finding rules")
	}
	for _, want := range []string{"# shared decisions", "path: fixtures/** # keep me", "value: kept"} {
		if !strings.Contains(string(after), want) {
			t.Errorf("withdrawal dropped preserved content %q", want)
		}
	}
	if strings.Contains(string(after), "identified") || strings.Contains(string(after), "unlabelled") {
		t.Fatal("withdrawn declarations remain in the file")
	}
	entries, defects, err := ReadSecrets(root.Path)
	testutil.FailErr(t, "read remaining entries", err)
	if len(entries) != 0 || len(defects) != 1 {
		t.Fatalf("entries=%d defects=%d", len(entries), len(defects))
	}
}
