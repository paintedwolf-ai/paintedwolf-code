package sinks

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/exposure"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
	"gopkg.in/yaml.v3"
)

// plant is one recognised secret and the catalog rule it stands for.
type plant struct {
	rule, value string
}

// sinkExclusions are the places a planted value may be found, each with why.
// The list is closed: a value found anywhere else fails.
var sinkExclusions = map[string]string{
	"backups": "backups keep conversation and project content without redaction (docs/privacy.md)",
}

// kingfisherRuleFile is the part of an upstream rule file the plants need.
type kingfisherRuleFile struct {
	Rules []struct {
		ID       string   `yaml:"id"`
		Examples []string `yaml:"examples"`
	} `yaml:"rules"`
}

type kingfisherManifest struct {
	Inventory struct {
		Skipped       map[string]int    `yaml:"skipped"`
		ExcludedRules map[string]string `yaml:"excluded_rules"`
	} `yaml:"inventory"`
}

// catalogPlants holds one recognised example for every imported catalog rule
// that carries examples. The matcher only locates the secret inside an
// example; the sinks are scanned without it.
func catalogPlants(t *testing.T) []plant {
	t.Helper()
	raw, err := config.Read(config.KingfisherManifest)
	testutil.FailErr(t, "read detector manifest", err)
	var manifest kingfisherManifest
	testutil.FailErr(t, "parse detector manifest", yaml.Unmarshal(raw, &manifest))
	if n := manifest.Inventory.Skipped["example_mismatch"]; n != 0 {
		t.Errorf("%d catalog rules are skipped because their examples do not match", n)
	}
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build matcher", err)
	var plants []plant
	var unrecognised []string
	err = config.Walk(config.KingfisherCatalogDir, func(path config.Rel, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yml") {
			return walkErr
		}
		body, err := config.Read(path)
		if err != nil {
			return err
		}
		var file kingfisherRuleFile
		if err := yaml.Unmarshal(body, &file); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		for _, rule := range file.Rules {
			if len(rule.Examples) == 0 || manifest.Inventory.ExcludedRules[rule.ID] != "" {
				continue
			}
			if value, ok := recognisedExample(matcher, rule.Examples); ok {
				plants = append(plants, plant{rule: rule.ID, value: value})
			} else {
				unrecognised = append(unrecognised, rule.ID)
			}
		}
		return nil
	})
	testutil.FailErr(t, "walk detector catalog", err)
	sort.Strings(unrecognised)
	if len(unrecognised) > 0 {
		t.Errorf("%d catalog rules have no example the matcher recognises: %v", len(unrecognised), unrecognised)
	}
	t.Logf("plants: %d catalog rules with a recognised example", len(plants))
	return plants
}

// recognisedExample returns the longest value the matcher recognises in the
// rule's first recognised example. Overlapping rules may claim the span; any
// recognised value must stay out of every sink.
func recognisedExample(matcher *secretmatch.Matcher, examples []string) (string, bool) {
	for _, example := range examples {
		runes := []rune(example)
		best := ""
		for _, hit := range matcher.Screen(example) {
			if hit.End <= len(runes) && hit.End-hit.Start > len([]rune(best)) {
				best = string(runes[hit.Start:hit.End])
			}
		}
		if best != "" {
			return best, true
		}
	}
	return "", false
}

// spellings are the forms a value may take in a sink.
func spellings(value string) []string {
	quoted, _ := json.Marshal(value)
	b := []byte(value)
	return []string{
		value,
		string(quoted[1 : len(quoted)-1]),
		base64.StdEncoding.EncodeToString(b),
		base64.URLEncoding.EncodeToString(b),
		base64.RawStdEncoding.EncodeToString(b),
		base64.RawURLEncoding.EncodeToString(b),
	}
}

func TestPlantedSecretsReachNoSink(t *testing.T) {
	// Debug logging and the model-request capture are sinks too.
	t.Setenv("LYCAON_LOG_LEVEL", "debug")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	plants := catalogPlants(t)
	managed := "pw-canary-managed-7f3c19e2b84d5a60"
	plants = append(plants, plant{rule: "managed secret", value: managed})

	project := t.TempDir()
	calls := plantFiles(t, project, plants)
	calls = append(calls, llm.MockToolCall{ID: "child-env", Name: "command", Args: map[string]any{"command": "env > child-env.txt"}})
	script := &llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: "read the planted files", ToolCalls: calls, FollowUpText: "Read them.",
	}}}
	hostDir := t.TempDir()
	h := wiring.BuildForTestAt(t, hostDir, wiring.WithRecordedScript(script))
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, project)
	testutil.FailErr(t, "create session", err)
	wiring.AttachDefaultAmbient(t, h, t.Context(), sess.ID)
	testutil.FailErr(t, "set agent type", h.Sessions.Manager.Chats.SetAgentType(t.Context(), sess.ID, "implementer"))
	h.SeedProgress(t, t.Context(), sess.ID)
	storeManagedSecret(t, h, sess.ProjectID, managed)

	envelopes := promptAnsweringCards(t, h, sess, "read the planted files")

	var values []string
	owner := map[string]string{}
	for _, p := range plants {
		for _, s := range spellings(p.value) {
			values = append(values, s)
			owner[s] = p.rule
		}
	}
	scanner := exposure.NewScanner(values...)
	leaks := map[string][]string{}
	note := func(sink string, found []string) {
		for _, value := range found {
			leaks[sink] = append(leaks[sink], owner[value])
		}
	}
	for i, req := range h.Recording.AllRequests() {
		raw, err := json.Marshal(req)
		testutil.FailErr(t, "encode request", err)
		note(fmt.Sprintf("model request %d", i), scanner.Found(string(raw)))
	}
	for i, env := range envelopes {
		note(fmt.Sprintf("event %d (%s)", i, env.Topic), scanner.Found(env.raw))
	}
	childEnv, err := os.ReadFile(filepath.Join(project, "child-env.txt"))
	testutil.FailErr(t, "read child environment", err)
	note("child environment", scanner.Found(string(childEnv)))
	for name, body := range diagnosticsBundle(t, h) {
		note("diagnostics "+name, scanner.Found(body))
	}
	excluded := map[string]bool{}
	err = filepath.WalkDir(hostDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || !entry.Type().IsRegular() {
			return walkErr
		}
		rel, _ := filepath.Rel(hostDir, path)
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		found := scanner.Found(string(body))
		if len(found) == 0 {
			return nil
		}
		for prefix := range sinkExclusions {
			if strings.HasPrefix(rel, prefix+string(filepath.Separator)) || strings.Contains(rel, string(filepath.Separator)+prefix+string(filepath.Separator)) {
				excluded[prefix] = true
				return nil
			}
		}
		note("file "+rel, found)
		return nil
	})
	testutil.FailErr(t, "walk host directory", err)
	t.Logf("scanned %d model requests, %d events, the host directory and diagnostics for %d values; excluded sinks holding values: %v",
		len(h.Recording.AllRequests()), len(envelopes), len(values), excluded)
	sinks := make([]string, 0, len(leaks))
	for sink := range leaks {
		sinks = append(sinks, sink)
	}
	sort.Strings(sinks)
	for _, sink := range sinks {
		rules := leaks[sink]
		sort.Strings(rules)
		if len(rules) > 8 {
			rules = append(rules[:8], fmt.Sprintf("and %d more", len(rules)-8))
		}
		t.Errorf("%s holds planted secrets from %v", sink, rules)
	}
}

// plantChunk bounds how many values one file or command carries.
const plantChunk = 50

// plantFiles writes every plant into an env file, a source file and a script
// that prints it, a chunk per file, and returns the calls that read them all.
func plantFiles(t *testing.T, project string, plants []plant) []llm.MockToolCall {
	t.Helper()
	var calls []llm.MockToolCall
	for start := 0; start < len(plants); start += plantChunk {
		chunk := plants[start:min(start+plantChunk, len(plants))]
		var env, source, emit strings.Builder
		emit.WriteString("#!/bin/sh\n")
		for i, p := range chunk {
			fmt.Fprintf(&env, "PLANT_%d=%s\n", start+i, p.value)
			quoted, _ := json.Marshal(p.value)
			fmt.Fprintf(&source, "PLANT_%d = %s\n", start+i, quoted)
			fmt.Fprintf(&emit, "printf '%%s\\n' %s\n", shellQuote(p.value))
		}
		n := start / plantChunk
		files := map[string]string{fmt.Sprintf(".env.%02d", n): env.String(), fmt.Sprintf("config_%02d.py", n): source.String(), fmt.Sprintf("emit_%02d.sh", n): emit.String()}
		for name, body := range files {
			testutil.FailErr(t, "plant "+name, os.WriteFile(filepath.Join(project, name), []byte(body), 0o644))
		}
		calls = append(calls,
			llm.MockToolCall{ID: fmt.Sprintf("read-env-%02d", n), Name: "command", Args: map[string]any{"command": fmt.Sprintf("cat .env.%02d", n)}},
			llm.MockToolCall{ID: fmt.Sprintf("read-source-%02d", n), Name: "command", Args: map[string]any{"command": fmt.Sprintf("cat config_%02d.py", n)}},
			llm.MockToolCall{ID: fmt.Sprintf("run-emitter-%02d", n), Name: "command", Args: map[string]any{"command": fmt.Sprintf("sh emit_%02d.sh", n)}})
	}
	return calls
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

type envelope struct {
	Topic wire.EventTopic
	raw   string
}

// promptAnsweringCards runs one turn, takes the recommended option on every
// card it raises, and returns every event the project published meanwhile.
func promptAnsweringCards(t *testing.T, h *wiring.Harness, sess *wire.Session, prompt string) []envelope {
	t.Helper()
	stream, unsubscribe, err := h.Events.Subscribe(t.Context(), events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	defer unsubscribe()
	done := make(chan error, 1)
	go func() {
		_, err := h.Sessions.Manager.Submissions.Prompt(t.Context(), sess.ID, prompt)
		done <- err
	}()
	var out []envelope
	answered := map[string]bool{}
	for {
		select {
		case err := <-done:
			testutil.FailErr(t, "prompt", err)
			return out
		case env := <-stream:
			raw, err := json.Marshal(env)
			testutil.FailErr(t, "encode event", err)
			out = append(out, envelope{Topic: env.Topic, raw: string(raw)})
			if env.Topic != wire.EventTopicCheckpoint {
				continue
			}
			data, err := json.Marshal(env.Data)
			testutil.FailErr(t, "encode checkpoint", err)
			var card wire.CheckpointEvent
			testutil.FailErr(t, "decode checkpoint", json.Unmarshal(data, &card))
			t.Logf("checkpoint %s kind %s status %s", card.ID, card.Kind, card.Status)
			if card.Status != wire.CheckpointStatusPending || answered[card.ID] {
				continue
			}
			answered[card.ID] = true
			if card.ToolApproval == nil {
				_, err = h.Sessions.Checkpoints.ResolveCheckpoint(h.OwnerCtx(t, t.Context()), card.SessionID, card.ID, card.Kind,
					&hitl.DecisionResult{Approved: true}, nil)
			} else {
				_, err = h.Sessions.Checkpoints.(*hitl.Checkpoints).Authority.ResolveApprovalOption(h.OwnerCtx(t, t.Context()),
					card.SessionID, card.ID, card.ToolApproval.Plan.RecommendedOptionID)
			}
			testutil.FailErr(t, "answer card", err)
		}
	}
}

func storeManagedSecret(t *testing.T, h *wiring.Harness, projectID, value string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"name": "canary", "purpose": "secret canary", "secret_value": value, "operation_id": uuid.NewString()})
	rec := serve(t, h, http.MethodPost, "/v1/projects/"+projectID+"/secrets", body)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("store managed secret = %d: %s", rec.Code, rec.Body.String())
	}
}

// diagnosticsBundle returns each entry of the diagnostics export, decompressed.
func diagnosticsBundle(t *testing.T, h *wiring.Harness) map[string]string {
	t.Helper()
	rec := serve(t, h, http.MethodGet, "/v1/diagnostics/export", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("diagnostics export = %d: %s", rec.Code, rec.Body.String())
	}
	archive, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	testutil.FailErr(t, "open diagnostics bundle", err)
	out := map[string]string{}
	for _, f := range archive.File {
		r, err := f.Open()
		testutil.FailErr(t, "open "+f.Name, err)
		body, err := io.ReadAll(r)
		_ = r.Close()
		testutil.FailErr(t, "read "+f.Name, err)
		out[f.Name] = string(body)
	}
	return out
}

func serve(t *testing.T, h *wiring.Harness, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+h.APIToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.Server.ServeHTTP(rec, req)
	return rec
}
