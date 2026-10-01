package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

type sidecarClient struct {
	base  string
	token string
	http  *http.Client
}

func (c *sidecarClient) request(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer closeBody(resp.Body)
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if output != nil && len(data) > 0 {
		if err := json.Unmarshal(data, output); err != nil {
			return fmt.Errorf("decode %s %s: %w", method, path, err)
		}
	}
	return nil
}

func (c *sidecarClient) session(ctx context.Context, id string) (api.Session, error) {
	var session api.Session
	err := c.request(ctx, http.MethodGet, "/v1/sessions/"+id, nil, &session)
	return session, err
}

func (c *sidecarClient) bootstrap(ctx context.Context, id string) (api.SessionBootstrap, error) {
	var bootstrap api.SessionBootstrap
	err := c.request(ctx, http.MethodGet, "/v1/sessions/"+id+"/bootstrap", nil, &bootstrap)
	return bootstrap, err
}

func (c *sidecarClient) waitPrepared(ctx context.Context, id string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		session, err := c.session(ctx, id)
		if err != nil {
			return err
		}
		switch session.Status {
		case api.SessionStatusIdle:
			return nil
		case api.SessionStatusError:
			return fmt.Errorf("session %s preparation failed", id)
		case api.SessionStatusPreparing, api.SessionStatusBusy:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *sidecarClient) waitPromptSettled(ctx context.Context, id string, baseline map[string]struct{}) (api.SessionBootstrap, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		bootstrap, err := c.bootstrap(ctx, id)
		if err != nil {
			return api.SessionBootstrap{}, err
		}
		if bootstrap.Session.Status == api.SessionStatusIdle && hasNewAssistant(bootstrap.Transcript.Messages, baseline) {
			return bootstrap, nil
		}
		if bootstrap.Session.Status == api.SessionStatusError {
			return api.SessionBootstrap{}, fmt.Errorf("session %s entered error state", id)
		}
		select {
		case <-ctx.Done():
			return api.SessionBootstrap{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func messageIDs(messages []api.Message) map[string]struct{} {
	ids := make(map[string]struct{}, len(messages))
	for _, message := range messages {
		if id := strings.TrimSpace(message.ID); id != "" {
			ids[id] = struct{}{}
		}
	}
	return ids
}

func hasNewAssistant(messages []api.Message, baseline map[string]struct{}) bool {
	for _, message := range messages {
		id := strings.TrimSpace(message.ID)
		if message.Role == api.MessageRoleAssistant && id != "" {
			if _, existed := baseline[id]; !existed {
				return true
			}
		}
	}
	return false
}

func newAssistantMessageID(messages []api.Message, baseline map[string]struct{}) string {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		id := strings.TrimSpace(message.ID)
		if message.Role == api.MessageRoleAssistant && id != "" {
			if _, existed := baseline[id]; !existed {
				return id
			}
		}
	}
	return ""
}

func (c *sidecarClient) replayStream(ctx context.Context, sessionID, messageID string) error {
	path := "/v1/sessions/" + sessionID + "/stream?message=" + url.QueryEscape(messageID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer closeBody(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("stream status %d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	done := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var frame struct {
			Done bool `json:"done"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
			return err
		}
		done = done || frame.Done
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !done {
		return fmt.Errorf("stream ended without done frame")
	}
	return nil
}

type eventMonitor struct {
	mu         sync.Mutex
	seen       map[string]struct{}
	count      int
	duplicates int
	lastCursor string
	err        error
	done       chan struct{}
}

var errEventReplayUnavailable = errors.New("event replay unavailable")

func (c *sidecarClient) startEventMonitor(ctx context.Context, projectID, after string) (*eventMonitor, error) {
	path := "/v1/events?project_id=" + url.QueryEscape(projectID)
	if strings.TrimSpace(after) != "" {
		path += "&after=" + url.QueryEscape(after)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := (&http.Client{}).Do(req) //nolint:bodyclose // The monitor goroutine closes a successful stream.
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer closeBody(resp.Body)
		if resp.StatusCode == http.StatusConflict {
			return nil, errEventReplayUnavailable
		}
		return nil, fmt.Errorf("event stream status %d", resp.StatusCode)
	}
	monitor := &eventMonitor{seen: make(map[string]struct{}), done: make(chan struct{})}
	go func() {
		defer close(monitor.done)
		defer closeBody(resp.Body)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var envelope api.EventEnvelope
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &envelope); err != nil {
				monitor.setError(err)
				return
			}
			monitor.mu.Lock()
			monitor.count++
			if _, exists := monitor.seen[envelope.EventID]; exists {
				monitor.duplicates++
			}
			monitor.seen[envelope.EventID] = struct{}{}
			monitor.lastCursor = envelope.Cursor
			monitor.mu.Unlock()
		}
		if err := scanner.Err(); err != nil && ctx.Err() == nil {
			monitor.setError(err)
		}
	}()
	return monitor, nil
}

func closeBody(body io.Closer) {
	if body != nil {
		_ = body.Close()
	}
}

func (m *eventMonitor) setError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func (m *eventMonitor) snapshot() (count, duplicates int, lastCursor string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.count, m.duplicates, m.lastCursor, m.err
}

func (m *eventMonitor) countSnapshot() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.count
}
