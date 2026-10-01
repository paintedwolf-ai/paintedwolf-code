package bundled

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"

	"github.com/lycaon/lycaon/internal/exec"
)

const maxVerifierOutputBytes = 2 << 20

type releaseCommand func(context.Context, ...string) ([]byte, error)

func githubReleaseCommand() (releaseCommand, error) {
	path, err := exec.LookPath("gh")
	if err != nil {
		return nil, fmt.Errorf("release selection requires maintained GitHub CLI attestation verification: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, args ...string) ([]byte, error) {
		output := &releaseCommandOutput{limit: maxVerifierOutputBytes}
		result, err := exec.RunPipeline(ctx, []exec.Stage{{Name: path, Args: args}}, exec.ExecOpts{
			Launch:    exec.HostLaunch("release attestation verification"),
			AppendEnv: []string{"GH_HOST=github.com", "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1"},
			Stdout:    output, MaxOutputBytes: 64 << 10,
		})
		if err != nil {
			return nil, fmt.Errorf("verification command for GitHub failed: %w", err)
		}
		if result.ExitCode != 0 {
			return nil, fmt.Errorf("verification command for GitHub exited with status %d", result.ExitCode)
		}
		return output.buffer.Bytes(), nil
	}, nil
}

type releaseCommandOutput struct {
	buffer bytes.Buffer
	limit  int
}

func (output *releaseCommandOutput) Write(raw []byte) (int, error) {
	if len(raw) > output.limit-output.buffer.Len() {
		return 0, fmt.Errorf("verifier output for GitHub exceeds limit")
	}
	return output.buffer.Write(raw)
}

func checkImmutableRelease(ctx context.Context, run releaseCommand, tag string) (jsonRecord []byte, err error) {
	repository, err := run(ctx, "api", "--hostname", "github.com", "repos/"+releaseRepository)
	if err != nil {
		return nil, err
	}
	var repo struct {
		ID    int64 `json:"id"`
		Owner struct {
			ID int64 `json:"id"`
		} `json:"owner"`
	}
	if err := decodeVerifierJSON(repository, &repo); err != nil {
		return nil, err
	}
	if strconv.FormatInt(repo.ID, 10) != releaseRepositoryID || strconv.FormatInt(repo.Owner.ID, 10) != releaseOwnerID {
		return nil, fmt.Errorf("release repository identity changed")
	}
	raw, err := run(ctx, "api", "--hostname", "github.com", "repositories/"+releaseRepositoryID+"/releases/tags/"+url.PathEscape(tag))
	if err != nil {
		return nil, err
	}
	var release struct {
		ID        int64  `json:"id"`
		Tag       string `json:"tag_name"`
		Draft     bool   `json:"draft"`
		Immutable bool   `json:"immutable"`
	}
	if err := decodeVerifierJSON(raw, &release); err != nil {
		return nil, err
	}
	if release.ID <= 0 || release.Tag != tag || release.Draft || !release.Immutable {
		return nil, fmt.Errorf("selection requires the exact published immutable release")
	}
	return raw, nil
}

func authenticateReleaseFile(ctx context.Context, run releaseCommand, path, digest, tag, commit string) (releaseSubjectEvidence, error) {
	evidence := releaseSubjectEvidence{SHA256: digest}
	raw, err := run(ctx, "attestation", "verify", path, "--repo", releaseRepository,
		"--hostname", "github.com",
		"--cert-oidc-issuer", releaseIssuer, "--cert-identity", "https://github.com/"+releaseWorkflow+"@refs/tags/"+tag,
		"--source-digest", commit, "--signer-digest", commit, "--source-ref", "refs/tags/"+tag,
		"--deny-self-hosted-runners", "--predicate-type", "https://slsa.dev/provenance/v1", "--format", "json")
	if err != nil {
		return evidence, err
	}
	if err := verifyWorkflowClaims(raw, tag, commit, digest); err != nil {
		return evidence, err
	}
	evidence.Workflow = raw
	raw, err = run(ctx, "release", "verify-asset", tag, path, "--repo", "github.com/"+releaseRepository, "--format", "json")
	if err != nil {
		return evidence, err
	}
	if err := verifyReleaseClaims(raw, digest); err != nil {
		return evidence, err
	}
	evidence.Release = raw
	return evidence, nil
}
