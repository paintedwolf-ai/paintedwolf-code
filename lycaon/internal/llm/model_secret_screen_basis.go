package llm

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// screenBasis captures the private one-attempt evidence and policy state of one Screen pass.
type screenBasis struct {
	valid             bool
	destination       ScreenDestination
	projectID         string
	rootSessionID     string
	payloadDigest     [32]byte
	policyRevision    string
	evidenceDigest    [32]byte
	harvestDigest     [32]byte
	classification    map[secretmatch.SecretFingerprint]bool
	classificationRev string
}

type revalidatingSecretScreen interface {
	OutboundSecretScreen
	ScreenWithBasis(ctx context.Context, destination ScreenDestination, policyRev string, req modelcall.CompletionRequest) (modelcall.CompletionRequest, screenBasis, error)
	FreshBasis(ctx context.Context, destination ScreenDestination, policyRev string, req modelcall.CompletionRequest, basis screenBasis) bool
}

// FreshBasis reports whether destination, policy, request payload, classifications,
// capability grants, and harvested evidence remain identical after a contention wait.
func (s *ModelSecretScreen) FreshBasis(
	ctx context.Context,
	destination ScreenDestination,
	policyRev string,
	req modelcall.CompletionRequest,
	basis screenBasis,
) bool {
	if s == nil {
		return basis.valid && destination == basis.destination && policyRev == basis.policyRevision &&
			requestScreenDigest(req) == basis.payloadDigest
	}
	if !basis.valid || destination != basis.destination || policyRev != basis.policyRevision {
		return false
	}
	if requestScreenDigest(req) != basis.payloadDigest {
		return false
	}
	attr := curationctx.SessionFrom(ctx)
	sessionID := firstNonEmpty(req.Debug.SessionID, attr.SessionID)
	projectID := firstNonEmpty(req.Debug.ProjectID, attr.ProjectID)
	projectDir := firstNonEmpty(req.Debug.ProjectDir, attr.ProjectDir)
	rootSessionID := firstNonEmpty(req.Debug.RootSessionID, req.Debug.ParentSessionID, attr.ParentSessionID, sessionID)
	if projectID != basis.projectID || rootSessionID != basis.rootSessionID {
		return false
	}
	ctx = secretmatch.WithAskAttribution(ctx, secretmatch.AskAttribution{
		SessionID: sessionID, RootSessionID: rootSessionID,
		ProjectID: projectID, ProjectDir: projectDir,
	})
	var values []secretmatch.Remembered
	if s.evidence != nil && projectID != "" {
		var err error
		values, err = s.evidence(ctx, projectID, rootSessionID)
		if err != nil {
			return false
		}
	}
	if rememberedDigest(values) != basis.evidenceDigest {
		return false
	}
	if s.matcher.Inert() {
		return true
	}
	if err := s.matcher.CheckClassification(ctx, basis.classification); err != nil {
		return false
	}
	if s.matcher.ClassificationRevision(ctx) != basis.classificationRev {
		return false
	}
	return s.matcher.HarvestDigest(ctx) == basis.harvestDigest
}

func snapshotScreenBasis(
	ctx context.Context,
	s *ModelSecretScreen,
	destination ScreenDestination,
	policyRev string,
	originalReq modelcall.CompletionRequest,
	projectID, rootSessionID string,
	classification map[secretmatch.SecretFingerprint]bool,
	classificationRev string,
	values []secretmatch.Remembered,
) screenBasis {
	basis := screenBasis{
		valid:             true,
		destination:       destination,
		projectID:         projectID,
		rootSessionID:     rootSessionID,
		payloadDigest:     requestScreenDigest(originalReq),
		policyRevision:    policyRev,
		evidenceDigest:    rememberedDigest(values),
		classification:    classification,
		classificationRev: classificationRev,
	}
	if s != nil && !s.matcher.Inert() {
		basis.harvestDigest = s.matcher.HarvestDigest(ctx)
	}
	return basis
}

func requestScreenDigest(req modelcall.CompletionRequest) [32]byte {
	h := sha256.New()
	writeStr := func(s string) {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(len(s))) // #nosec G115 -- request strings fit within memory bounds.
		_, _ = h.Write(b[:])
		_, _ = h.Write([]byte(s))
	}
	writeStr(req.Model)
	writeStr(string(req.Think))
	writeStr(string(req.Composition))
	if req.ThinkingOverride != nil {
		if raw, err := json.Marshal(req.ThinkingOverride); err == nil {
			writeStr("override:" + string(raw))
		}
	}
	if req.ResponseFormat != nil {
		if raw, err := json.Marshal(req.ResponseFormat); err == nil {
			writeStr(string(raw))
		}
	}
	if raw, err := json.Marshal(req.Messages); err == nil {
		_, _ = h.Write(raw)
	}
	if raw, err := json.Marshal(req.Tools); err == nil {
		_, _ = h.Write(raw)
	}
	return sha256.Sum256(h.Sum(nil))
}

func rememberedDigest(values []secretmatch.Remembered) [32]byte {
	if len(values) == 0 {
		return [32]byte{}
	}
	items := slices.Clone(values)
	slices.SortFunc(items, func(a, b secretmatch.Remembered) int {
		if c := strings.Compare(a.Secret, b.Secret); c != 0 {
			return c
		}
		if c := strings.Compare(a.Reference, b.Reference); c != 0 {
			return c
		}
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.RuleID, b.RuleID)
	})
	h := sha256.New()
	writeStr := func(s string) {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(len(s))) // #nosec G115 -- secret sizes are bounded by MaxSecretBytes.
		_, _ = h.Write(b[:])
		_, _ = h.Write([]byte(s))
	}
	for _, v := range items {
		writeStr(v.Secret)
		writeStr(v.Name)
		writeStr(v.Origin)
		writeStr(v.RuleID)
		writeStr(v.Title)
		writeStr(string(v.Source))
		writeStr(v.Reference)
		var flags [2]byte
		if v.Retired {
			flags[0] = 1
		}
		if v.NonDisclosable {
			flags[1] = 1
		}
		_, _ = h.Write(flags[:])
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}
