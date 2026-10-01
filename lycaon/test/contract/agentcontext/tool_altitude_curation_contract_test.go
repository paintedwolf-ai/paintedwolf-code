package contract

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestAltitudeInvariantCuratorSnapshotIsolation(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	snapshot := snapshotFromReadLine(t, "current.go", "5| current only", 5)
	provider := &stubCuratorProvider{
		id: "lite",
		responses: []string{
			`{"selections":[{"path":"prior.go","line":1,"excerpt":"prior survey"}],"gloss":[]}`,
		},
	}
	cur, err := llm.NewTestRegistrySummarizer(provider)
	contractcheck.FailErr(t, "NewTestRegistrySummarizer", err)
	got, err := cur.Curate(context.Background(), snapshot, llm.CurationFocus{Target: "focus"}, 3)
	contractcheck.FailErr(t, "Curate", err)
	if len(got.Selections) != 0 {
		t.Fatalf("selection outside snapshot must be dropped: %#v", got.Selections)
	}
}

func TestAltitudeInvariantExpressedScopeLiteral(t *testing.T) {
	t.Parallel()
	boundary := altitudeBoundary(t)
	ctx := context.Background()

	t.Run("read_offset", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		contractcheck.FailErr(t, "write", os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644))
		tool := &surveytools.ReadTool{Boundary: boundary}
		args := map[string]any{"path": "main.go", "offset": 1, "limit": 2}
		out, err := tool.Run(ctx, args, altitudeCtx(tmp, "lit-read"))
		contractcheck.FailErr(t, "read", err)
		out2, err := tool.Run(ctx, args, altitudeCtx(tmp, "lit-read"))
		contractcheck.FailErr(t, "read rerun", err)
		if canonicalSurveyJSON(t, out) != canonicalSurveyJSON(t, out2) {
			t.Fatal("literal read must be byte-stable across calls")
		}
		if evidence.ReadEvidenceSurvey(args, out) {
			t.Fatal("expressed read must not be survey-grade")
		}
		assertNoCuratedFields(t, out)
	})

	t.Run("list_dir_targeted", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		contractcheck.FailErr(t, "mkdir", os.Mkdir(filepath.Join(tmp, "subdir"), 0o755))
		contractcheck.FailErr(t, "write", os.WriteFile(filepath.Join(tmp, "a.txt"), []byte("hi"), 0o644))
		tool := &surveytools.ListDirTool{Boundary: boundary}
		args := map[string]any{"path": ".", "max_depth": 1}
		out, err := tool.Run(ctx, args, altitudeCtx(tmp, "lit-list"))
		contractcheck.FailErr(t, "list_dir", err)
		if evidence.ListEvidenceSurvey(args, canonicalSurveyJSON(t, out)) {
			t.Fatal("targeted list_dir must not be survey-grade")
		}
		if !strings.Contains(out, `"entries"`) {
			t.Fatalf("targeted list_dir must return entries[]: %s", out)
		}
		assertNoCuratedFields(t, out)
	})

	t.Run("grep_narrow", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		contractcheck.FailErr(t, "write", os.WriteFile(filepath.Join(tmp, "a.txt"), []byte("alpha\nbeta\n"), 0o644))
		tool := &surveytools.GrepTool{Boundary: boundary}
		args := map[string]any{"pattern": "alpha"}
		out, err := tool.Run(ctx, args, altitudeCtx(tmp, "lit-grep"))
		contractcheck.FailErr(t, "grep", err)
		if evidence.GrepEvidenceSurvey(args, canonicalSurveyJSON(t, out)) {
			t.Fatal("narrow grep must not be survey-grade")
		}
		assertNoCuratedFields(t, out)
	})

	t.Run("find_narrow", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		contractcheck.FailErr(t, "write", os.WriteFile(filepath.Join(tmp, "only.txt"), []byte("x"), 0o644))
		tool := &surveytools.FindTool{Boundary: boundary}
		args := map[string]any{"type": "file"}
		out, err := tool.Run(ctx, args, altitudeCtx(tmp, "lit-find"))
		contractcheck.FailErr(t, "find", err)
		if evidence.FindEvidenceSurvey(args, canonicalSurveyJSON(t, out)) {
			t.Fatal("narrow find must not be survey-grade")
		}
		assertNoCuratedFields(t, out)
	})

	t.Run("grep_paged", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		writeGrepHits(t, tmp, safecmd.GrepMaxMatches+50)
		tool := &surveytools.GrepTool{Boundary: boundary}
		args := map[string]any{"pattern": "needle", "offset": float64(200)}
		out, err := tool.Run(ctx, args, altitudeCtx(tmp, "lit-grep-page"))
		contractcheck.FailErr(t, "grep page", err)
		if evidence.GrepEvidenceSurvey(args, canonicalSurveyJSON(t, out)) {
			t.Fatal("paged grep must not be survey-grade")
		}
		assertNoCuratedFields(t, out)
	})
}

func TestAltitudeInvariantNoCommissionAdversarial(t *testing.T) {
	t.Parallel()
	boundary := altitudeBoundary(t)
	ctx := context.Background()

	cases := []struct {
		name string
		run  func(t *testing.T, dir string) string
	}{
		{
			name: "read",
			run: func(t *testing.T, dir string) string {
				writeLargeGoFile(t, dir, "big.go")
				tool := &surveytools.ReadTool{Boundary: boundary, Escalation: surveytools.NewReadEscalationStore()}
				out, err := tool.Run(ctx, map[string]any{"path": "big.go"}, altitudeCtx(dir, "adv-read"))
				contractcheck.FailErr(t, "read", err)
				return out
			},
		},
		{
			name: "list_dir",
			run: func(t *testing.T, dir string) string {
				contractcheck.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
				tool := &surveytools.ListDirTool{Boundary: boundary}
				out, err := tool.Run(ctx, map[string]any{"path": "."}, altitudeCtx(dir, "adv-list"))
				contractcheck.FailErr(t, "list_dir", err)
				return out
			},
		},
		{
			name: "grep",
			run: func(t *testing.T, dir string) string {
				writeGrepHits(t, dir, safecmd.GrepMaxMatches+50)
				tool := &surveytools.GrepTool{Boundary: boundary}
				out, err := tool.Run(ctx, map[string]any{"pattern": "needle"}, altitudeCtx(dir, "adv-grep"))
				contractcheck.FailErr(t, "grep", err)
				return out
			},
		},
		{
			name: "find",
			run: func(t *testing.T, dir string) string {
				writeManyFindFiles(t, dir, safecmd.FindMaxResults+50)
				tool := &surveytools.FindTool{Boundary: boundary}
				out, err := tool.Run(ctx, map[string]any{"type": "file"}, altitudeCtx(dir, "adv-find"))
				contractcheck.FailErr(t, "find", err)
				return out
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			out := tc.run(t, dir)
			if strings.Contains(out, `"highlights"`) {
				t.Fatalf("Tier 1 overflow must not surface highlights: %s", out)
			}
			var wrap struct {
				Selected int `json:"selected"`
			}
			_ = json.Unmarshal([]byte(out), &wrap)
			if wrap.Selected != 0 {
				t.Fatalf("selected = %d want 0 for Tier 1 overflow", wrap.Selected)
			}
		})
	}
}

func TestAltitudeInvariantNoCommissionMatchedOnly(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	snapshot := snapshotFromReadLine(t, "pkg/a.go", "10| func entry() {}", 10)
	provider := &stubCuratorProvider{
		id: "lite",
		responses: []string{
			`{"selections":[{"path":"pkg/a.go","line":10,"excerpt":"func entry"}],"gloss":[{"label":"entry"}]}`,
		},
	}
	cur, err := llm.NewTestRegistrySummarizer(provider)
	contractcheck.FailErr(t, "NewTestRegistrySummarizer", err)
	got, err := cur.Curate(context.Background(), snapshot, llm.CurationFocus{Target: "entry"}, 3)
	contractcheck.FailErr(t, "Curate", err)
	for _, sel := range got.Selections {
		if sel.Resolution.Verdict != evidence.VerdictMatched {
			t.Fatalf("curated selection must be matched verbatim: %#v", sel)
		}
	}
}

func canonicalSurveyJSON(t *testing.T, raw string) string {
	t.Helper()
	var v any
	contractcheck.FailErr(t, "unmarshal tool out", json.Unmarshal([]byte(raw), &v))
	out, err := surveyjson.Marshal(v)
	contractcheck.FailErr(t, "marshal canonical", err)
	return string(out)
}

func sortedSchemaPropertyKeys(schema map[string]any) []string {
	props, _ := schema["properties"].(map[string]any)
	var keys []string
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
