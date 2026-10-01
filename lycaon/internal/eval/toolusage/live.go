package toolusage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/eval/episode"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// DefaultLiveTimeout leaves preparation and model work bounded only by cancellation.
const DefaultLiveTimeout time.Duration = 0

// LiveOptions configures a headless live corpus run.
type LiveOptions struct {
	AllowLive  bool
	BaseURL    string
	Token      string
	ProjectDir string
	Corpus     *Corpus
	Runs       int
	Timeout    time.Duration
	CaptureDir string
}

// RunLive executes the corpus against a running sidecar and returns an aggregated profile.
func RunLive(ctx context.Context, opts LiveOptions) (Profile, error) {
	if !opts.AllowLive {
		return Profile{}, fmt.Errorf("live evaluation spends real tokens; explicitly enable --allow-live")
	}
	if opts.Corpus == nil {
		return Profile{}, fmt.Errorf("corpus is required")
	}
	if opts.Runs < 1 {
		opts.Runs = 1
	}
	if opts.Timeout < 0 {
		return Profile{}, fmt.Errorf("task timeout cannot be negative")
	}
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		return Profile{}, fmt.Errorf("base URL is required for live mode")
	}
	token := strings.TrimSpace(opts.Token)
	if token == "" {
		token = strings.TrimSpace(os.Getenv("LYCAON_API_TOKEN"))
	}
	if token == "" {
		token = strings.TrimSpace(os.Getenv("LYCAON_E2E_TOKEN"))
	}
	if token == "" {
		return Profile{}, fmt.Errorf("API token required (LYCAON_API_TOKEN or --token)")
	}
	projectDir := strings.TrimSpace(opts.ProjectDir)
	if projectDir == "" {
		root, err := moduleRoot()
		if err != nil {
			return Profile{}, err
		}
		projectDir = filepath.Join(root, "test", "fixtures", "e2e", "minimal-go-project")
	}
	client := newLiveClient(base, token)
	captureDir := strings.TrimSpace(opts.CaptureDir)
	if captureDir == "" {
		captureDir = debugpaths.ActiveSessionDir()
	}
	if captureDir == "" {
		return Profile{}, fmt.Errorf("capture dir required before live evaluation; enable debug capture on the sidecar")
	}

	var runProfiles []Profile
	for run := 0; run < opts.Runs; run++ {
		proj, err := client.createProject(ctx, projectDir, "")
		if err != nil {
			return Profile{}, fmt.Errorf("run %d create project: %w", run+1, err)
		}
		rootSessionIDs := make([]string, 0, len(opts.Corpus.Tasks))
		for _, task := range opts.Corpus.Tasks {
			sess, err := client.createSession(ctx, wire.CreateSessionRequest{ProjectID: proj.ID})
			if err != nil {
				return Profile{}, fmt.Errorf("run %d task %q create session: %w", run+1, task.ID, err)
			}
			rootSessionIDs = append(rootSessionIDs, sess.ID)
			if err := client.runScenario(ctx, sess.ID, task, opts.Timeout); err != nil {
				if abortErr := client.abortScenario(ctx, sess.ID); abortErr != nil {
					return Profile{}, fmt.Errorf("run %d task %q: %w; abort failed: %w", run+1, task.ID, err, abortErr)
				}
				return Profile{}, fmt.Errorf("run %d task %q: %w", run+1, task.ID, err)
			}
		}
		prof, err := ProfileFromCaptureSessions(captureDir, rootSessionIDs)
		if err != nil {
			return Profile{}, fmt.Errorf("run %d profile capture: %w", run+1, err)
		}
		prof.CorpusID = opts.Corpus.ID
		prof.Runs = 1
		runProfiles = append(runProfiles, prof)
	}
	if len(runProfiles) == 1 {
		p := runProfiles[0]
		p.Runs = 1
		return p, nil
	}
	agg := AggregateProfiles(runProfiles)
	agg.CorpusID = opts.Corpus.ID
	return agg, nil
}

type liveClient struct {
	settlementDirectory string
	observeFailure      func(context.Context, string) error
	observeCompletion   func(context.Context, string, string) (bool, error)
	observeExecution    func(context.Context, string) (episode.Execution, error)
	onFollowUp          func()
	base                string
	token               string
	http                *http.Client
	onHumanInput        func([]wire.CheckpointEvent, *FeedbackRequest) error
}

func (c *liveClient) createProject(ctx context.Context, dir, name string) (wire.Project, error) {
	request := wire.CreateProjectRequest{Roots: []wire.CreateProjectRootInput{{Path: dir}}}
	if name = strings.TrimSpace(name); name != "" {
		request.Name = &name
	}
	body, err := json.Marshal(request)
	if err != nil {
		return wire.Project{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/projects", bytes.NewReader(body))
	if err != nil {
		return wire.Project{}, err
	}
	return decodeJSON[wire.Project](c.do(req)) //nolint:bodyclose // decodeJSON closes the body
}

func (c *liveClient) createSession(ctx context.Context, request wire.CreateSessionRequest) (wire.Session, error) {
	if request.Posture == "" {
		request.Posture = wire.SessionPostureBuild
	}
	body, err := json.Marshal(request)
	if err != nil {
		return wire.Session{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/sessions", bytes.NewReader(body))
	if err != nil {
		return wire.Session{}, err
	}
	return decodeJSON[wire.Session](c.do(req)) //nolint:bodyclose // decodeJSON closes the body
}

func (c *liveClient) postPromptAndWait(ctx context.Context, sessionID, text string, timeout time.Duration) error {
	ctx, cancel := taskContext(ctx, timeout)
	defer cancel()
	if err := c.waitSessionPrepared(ctx, sessionID); err != nil {
		return err
	}
	body, err := json.Marshal(wire.PromptRequest{OperationID: uuid.NewString(), Text: text})
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/sessions/"+sessionID+"/prompts", bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp, err := c.request(req, true)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("prompt status %d: %s", resp.StatusCode, string(b))
	}
	var accepted wire.PromptAcceptedResponse
	if err := json.NewDecoder(resp.Body).Decode(&accepted); err != nil {
		return fmt.Errorf("decode prompt admission: %w", err)
	}
	if accepted.OperationID == "" || accepted.MessageID == "" {
		return fmt.Errorf("prompt admission requires operation and message identities")
	}
	return c.waitScenarioCompletion(ctx, sessionID, func(ctx context.Context) (bool, error) {
		return c.promptSettled(ctx, sessionID, accepted)
	})
}

func (c *liveClient) waitScenarioCompletion(ctx context.Context, sessionID string, settled func(context.Context) (bool, error)) error {
	for {
		if c.observeFailure != nil {
			if err := c.observeFailure(ctx, sessionID); err != nil {
				return err
			}
		}
		if c.onHumanInput != nil {
			pending, err := c.pendingCheckpoints(ctx, sessionID)
			if err != nil {
				return err
			}
			feedback, err := c.pendingFeedback(ctx, sessionID)
			if err != nil {
				return err
			}
			if err := c.onHumanInput(pending, feedback); err != nil {
				return err
			}
		}
		complete, err := settled(ctx)
		if err != nil || complete {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (c *liveClient) promptSettled(ctx context.Context, sessionID string, accepted wire.PromptAcceptedResponse) (bool, error) {
	if c.observeCompletion != nil {
		return c.observeCompletion(ctx, sessionID, accepted.OperationID)
	}
	pre, err := c.getSession(ctx, sessionID)
	if err != nil {
		return false, err
	}
	if pre.Status == wire.SessionStatusError {
		return false, fmt.Errorf("session %s entered error state", sessionID)
	}
	messages, err := c.listMessages(ctx, sessionID)
	if err != nil {
		return false, err
	}
	post, err := c.getSession(ctx, sessionID)
	return err == nil && pre.Status == wire.SessionStatusIdle && post.Status == wire.SessionStatusIdle && promptReady(messages, accepted.MessageID), err
}

func (c *liveClient) waitSessionPrepared(ctx context.Context, sessionID string) error {
	for {
		sess, err := c.getSession(ctx, sessionID)
		if err != nil {
			return err
		}
		switch sess.Status {
		case wire.SessionStatusIdle:
			return nil
		case wire.SessionStatusPreparing:
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		default:
			return fmt.Errorf("session %s is not ready: %s", sessionID, sess.Status)
		}
	}
}

func (c *liveClient) getSession(ctx context.Context, sessionID string) (wire.Session, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/sessions/"+sessionID, nil)
	if err != nil {
		return wire.Session{}, err
	}
	return decodeJSON[wire.Session](c.do(req)) //nolint:bodyclose // decodeJSON closes the body
}

func (c *liveClient) listMessages(ctx context.Context, sessionID string) ([]wire.Message, error) {
	var messages []wire.Message
	var cursor string
	for {
		reqURL := fmt.Sprintf("/v1/sessions/%s/messages?from=oldest", sessionID)
		if cursor != "" {
			reqURL = fmt.Sprintf("/v1/sessions/%s/messages?after=%s", sessionID, url.QueryEscape(cursor))
		}
		req, err := c.newRequest(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, err
		}
		page, err := decodeJSON[wire.SessionTranscriptPage](c.do(req)) //nolint:bodyclose // decodeJSON closes the body
		if err != nil {
			return nil, err
		}
		messages = append(messages, page.Messages...)
		if page.AfterCursor == "" {
			return messages, nil
		}
		if page.AfterCursor == cursor {
			return nil, fmt.Errorf("transcript page did not advance after cursor %q", cursor)
		}
		cursor = page.AfterCursor
	}
}

func decodeJSON[T any](resp *http.Response, err error) (T, error) {
	var zero T
	if err != nil {
		return zero, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return zero, err
	}
	return out, nil
}

func promptReady(msgs []wire.Message, submissionID string) bool {
	if submissionID == "" {
		return false
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg.Role == wire.MessageRoleUser && msg.ID == submissionID {
			return finalAnswer(msgs[i+1:]) != ""
		}
	}
	return false
}

// DefaultLiveBaseURL resolves an explicitly configured harness address.
func DefaultLiveBaseURL() string {
	if v := strings.TrimSpace(os.Getenv("LYCAON_E2E_API_URL")); v != "" {
		return v
	}
	addr := strings.TrimSpace(os.Getenv("LYCAON_E2E_ADDR"))
	if addr == "" {
		return ""
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	return "http://" + addr
}
