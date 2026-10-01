package toolusage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type SandboxEvidence struct {
	PreparationCallID     string `json:"preparation_call_id,omitempty"`
	ObservedAfterFollowUp bool   `json:"observed_after_follow_up,omitempty"`
	beginFollowUp         func()
	Kind                  string `json:"kind"`
	Path                  string `json:"path,omitempty"`
	Port                  int    `json:"port,omitempty"`
	Receipt               string `json:"receipt"`
	Observed              bool   `json:"observed"`
	stop                  func()
}

func (s *SandboxEvidence) close() {
	if s.stop != nil {
		s.stop()
	}
}

// prepareSuiteSandbox stages the fixture boundary with its owned external root.
func prepareSuiteSandbox(ctx context.Context, spec *SuiteCase, result *CaseReport, outsideRoot string) error {
	if spec.Sandbox == "" {
		return nil
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	evidence := &SandboxEvidence{Kind: spec.Sandbox, Receipt: hex.EncodeToString(nonce)}
	result.Sandbox = evidence
	if spec.Sandbox == "loopback" || spec.Sandbox == "deny_loopback" {
		if spec.Sandbox == "deny_loopback" {
			body, err := os.ReadFile(filepath.Join(result.ProjectDir, "fallback.json"))
			if err != nil {
				return err
			}
			var fallback struct {
				Receipt string `json:"receipt"`
			}
			if err := json.Unmarshal(body, &fallback); err != nil {
				return err
			}
			if fallback.Receipt == "" {
				return fmt.Errorf("denied service requires a supplied fallback receipt")
			}
			evidence.Receipt = fallback.Receipt
		}
		return prepareLoopbackFixture(ctx, result.ProjectDir, evidence)
	}
	if spec.Sandbox == sandboxWriteRoot {
		outside := outsideRoot
		info, err := os.Stat(outside)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(outside) || !info.IsDir() {
			return fmt.Errorf("external write root must be an absolute directory")
		}
		evidence.Path = outside
		spec.Prompt = strings.ReplaceAll(spec.Prompt, "${sandbox_path}", evidence.Path)
		return nil
	}
	outside, err := os.MkdirTemp(filepath.Dir(result.ProjectDir), "shared-input-")
	if err != nil {
		return err
	}
	evidence.Path = filepath.Join(outside, "input.json")
	if err := writeSandboxJSON(outside, "input.json", map[string]string{"receipt": evidence.Receipt}); err != nil {
		return err
	}
	spec.Prompt = strings.ReplaceAll(spec.Prompt, "${sandbox_path}", evidence.Path)
	if spec.Sandbox == "deny_read" {
		// Without a fallback, the oracle requires the output artifact to stay absent.
		body, err := os.ReadFile(filepath.Join(result.ProjectDir, "fallback.json"))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		var fallback struct {
			Receipt string `json:"receipt"`
		}
		if err := json.Unmarshal(body, &fallback); err != nil {
			return err
		}
		if fallback.Receipt == "" {
			return fmt.Errorf("denied input requires a supplied fallback receipt")
		}
		evidence.Receipt = fallback.Receipt
	}
	return nil
}

func prepareLoopbackFixture(ctx context.Context, project string, evidence *SandboxEvidence) error {
	var config net.ListenConfig
	listener, err := config.Listen(ctx, "tcp", "127.0.0.1:0") // #nosec G102 -- owned loopback-only fixture.
	if err != nil {
		return err
	}
	evidence.Port = listener.Addr().(*net.TCPAddr).Port
	var observed, afterFollowUp, followUp atomic.Bool
	var receipt atomic.Value
	receipt.Store(evidence.Receipt)
	if evidence.Kind == "deny_loopback" {
		receipt.Store(rand.Text())
	}
	evidence.beginFollowUp = func() {
		if evidence.Kind == "deny_loopback" {
			return
		}
		evidence.Receipt = rand.Text()
		receipt.Store(evidence.Receipt)
		followUp.Store(true)
	}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if evidence.Kind == "deny_loopback" {
			observed.Store(true)
		}
		if r.Method != http.MethodGet || r.URL.Path != "/receipt" {
			http.NotFound(w, r)
			return
		}
		observed.Store(true)
		if followUp.Load() {
			afterFollowUp.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{"receipt": receipt.Load().(string)}); err != nil {
			slog.DebugContext(r.Context(), "Write sandbox receipt failed", "error", err)
		}
	})}
	evidence.stop = func() {
		_ = server.Close()
		evidence.Observed = observed.Load()
		evidence.ObservedAfterFollowUp = afterFollowUp.Load()
	}
	go func() { _ = server.Serve(listener) }()
	if err := writeSandboxJSON(project, "endpoint.json", map[string]string{"url": fmt.Sprintf("http://127.0.0.1:%d/receipt", evidence.Port)}); err != nil {
		evidence.close()
		return err
	}
	return nil
}

func writeSandboxJSON(root, name string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.Location{Root: root, Rel: name}, Source: bytes.NewReader(body), Mode: 0o600})
	return err
}

func fixtureApprovalOption(fixture *SandboxEvidence, checkpoint wire.CheckpointEvent) string {
	if fixture == nil || (fixture.Kind == "deny_read" || fixture.Kind == "deny_loopback") || checkpoint.ToolApproval == nil || checkpoint.ToolApproval.JoinedCount > 1 {
		return ""
	}
	plan := checkpoint.ToolApproval.Plan
	if !fixtureCheckpointMatches(fixture, checkpoint) {
		return ""
	}
	for _, option := range plan.Options {
		if (fixture.Kind == "loopback" || fixture.Kind == sandboxWriteRoot) && fixture.PreparationCallID != "" {
			if option.Kind == wire.ApprovalOptionKindLease && option.Scope == wire.ApprovalGrantScopeChat && option.Rung == wire.ApprovalOptionRungChat && option.DecisionAction == wire.ApprovalOptionDecisionApprove && !option.Disabled {
				return option.ID
			}
			continue
		}
		if option.Kind == wire.ApprovalOptionKindCurrentAction && option.Rung == wire.ApprovalOptionRungOnce && option.DecisionAction == wire.ApprovalOptionDecisionApprove && !option.Disabled {
			return option.ID
		}
	}
	return ""
}

func fixtureCheckpointMatches(fixture *SandboxEvidence, checkpoint wire.CheckpointEvent) bool {
	return fixture != nil && fixture.PreparationCallID != "" && checkpoint.ToolApproval != nil &&
		checkpoint.ToolApproval.JoinedCount <= 1 && checkpoint.ToolApproval.ToolCallID == fixture.PreparationCallID
}

func fixtureDenial(fixture *SandboxEvidence, checkpoint wire.CheckpointEvent) bool {
	return fixtureCheckpointMatches(fixture, checkpoint) && (fixture.Kind == "deny_read" || fixture.Kind == "deny_loopback")
}
