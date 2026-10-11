package maintainability

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/lycaon/lycaon/test/contract/internal/sizebudget"
)

const reviewDirectory = "lycaon/test/contract/maintainability-reviews"

type artifactReview struct {
	Category string `json:"category"`
	Artifact string `json:"artifact"`
	sizebudget.ArtifactReview
}

func attachReviews(tree *workingTree, policy sizebudget.Policy, inv *inventory, report *sizebudget.TrackingReport) error {
	rows := map[string]*sizebudget.TrackingArtifact{}
	for i := range report.Artifacts {
		row := &report.Artifacts[i]
		rows[row.Category+":"+row.ID] = row
	}
	seen := map[string]bool{}
	for _, file := range tree.files() {
		if !strings.HasPrefix(file, reviewDirectory+"/") {
			continue
		}
		review, err := readReview(tree, file)
		if err != nil {
			return err
		}
		key := review.Category + ":" + review.Artifact
		if seen[key] {
			return fmt.Errorf("duplicate maintainability review %s", key)
		}
		seen[key] = true
		if err := validateReview(review, policy, inv); err != nil {
			return fmt.Errorf("review %s: %w", file, err)
		}
		review.State = reviewState(tree, review, inv.measured[review.Category][review.Artifact])
		if row := rows[key]; row != nil {
			row.Review = &review.ArtifactReview
		}
	}
	return nil
}

func readReview(tree *workingTree, file string) (artifactReview, error) {
	var review artifactReview
	if !tree.regular(file) || !strings.HasSuffix(file, ".json") {
		return review, fmt.Errorf("invalid review file %s", file)
	}
	raw, err := tree.read(file)
	if err != nil {
		return review, fmt.Errorf("read review %s: %w", file, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&review); err != nil {
		return review, fmt.Errorf("decode review %s: %w", file, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return review, fmt.Errorf("review %s has trailing content", file)
	}
	return review, nil
}

func validateReview(review artifactReview, policy sizebudget.Policy, inv *inventory) error {
	limit, ok := policy.Limits[review.Category]
	if !ok {
		return fmt.Errorf("unknown category %q", review.Category)
	}
	if _, ok := inv.measured[review.Category][review.Artifact]; !ok {
		return fmt.Errorf("artifact %q no longer exists; remove or revise its review", review.Artifact)
	}
	if !reviewPath(review.Artifact) || !reviewSlug(review.Workstream) {
		return fmt.Errorf("invalid artifact or workstream")
	}
	switch review.Kind {
	case "runtime_service", "construction", "data_record", "projection", "algorithm", "test_suite", "module":
	default:
		return fmt.Errorf("unknown kind %q", review.Kind)
	}
	if review.Disposition != "refactor" && review.Disposition != "retain" {
		return fmt.Errorf("unknown disposition %q", review.Disposition)
	}
	if strings.TrimSpace(review.Reason) == "" || len(review.Reason) > 4096 || !hexLength(review.ReviewedCommit, 40) {
		return fmt.Errorf("reason and reviewed commit are required")
	}
	if review.State != "" {
		return fmt.Errorf("review state is measured, not authored")
	}
	if review.Disposition == "refactor" {
		if review.Ceiling != 0 || len(review.Boundaries) != 0 {
			return fmt.Errorf("refactor reviews cannot accept a ceiling or boundaries")
		}
		return nil
	}
	if review.Ceiling <= limit.Warn || review.Ceiling > limit.Limit || len(review.Boundaries) == 0 || len(review.Boundaries) > 32 {
		return fmt.Errorf("retained warning requires a ceiling within the hard limit and 1-32 boundaries")
	}
	for file, digest := range review.Boundaries {
		if !reviewPath(file) || !hexLength(digest, 64) {
			return fmt.Errorf("invalid boundary %q", file)
		}
	}
	return nil
}

func reviewState(tree *workingTree, review artifactReview, measured int) string {
	if review.Disposition == "refactor" {
		return "refactor"
	}
	if measured > review.Ceiling {
		return "ceiling_exceeded"
	}
	for file, expected := range review.Boundaries {
		raw, err := tree.read(file)
		if err != nil || !tree.regular(file) || fmt.Sprintf("%x", sha256.Sum256(raw)) != expected {
			return "boundary_changed"
		}
	}
	return "retained"
}

func reviewPath(value string) bool {
	return value != "" && len(value) <= 1024 && value != "." && path.Clean(value) == value &&
		!strings.HasPrefix(value, "/") && value != ".." && !strings.HasPrefix(value, "../") &&
		!strings.ContainsAny(value, "\\\x00\r\n\t`")
}

func reviewSlug(value string) bool {
	if value == "" || len(value) > 80 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func hexLength(value string, length int) bool {
	_, err := hex.DecodeString(value)
	return len(value) == length && err == nil && value == strings.ToLower(value)
}
