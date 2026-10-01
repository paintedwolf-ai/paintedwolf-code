package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

func projectFromListRow(row db.ListProjectsRow, rootRows []db.ProjectRoots) (*Project, error) {
	return projectFromStatsFields(
		row.ID,
		row.Name,
		row.LastOpenedAt,
		row.CreatedAt,
		row.RootsGeneration,
		row.Starred,
		row.CoverArtifactID,
		row.CoverRootSessionID,
		row.CoverSource,
		row.CoverUpdatedAt,
		row.TrustEnabled,
		row.TrustReadBaseline,
		row.SessionCount,
		row.LastActivityAt,
		rootRows,
	)
}

func projectFromGetStatsRow(row db.GetProjectWithStatsRow, rootRows []db.ProjectRoots) (*Project, error) {
	return projectFromStatsFields(
		row.ID,
		row.Name,
		row.LastOpenedAt,
		row.CreatedAt,
		row.RootsGeneration,
		row.Starred,
		row.CoverArtifactID,
		row.CoverRootSessionID,
		row.CoverSource,
		row.CoverUpdatedAt,
		row.TrustEnabled,
		row.TrustReadBaseline,
		row.SessionCount,
		row.LastActivityAt,
		rootRows,
	)
}

func projectFromStatsFields(
	id string,
	name sql.NullString,
	lastOpenedAt string,
	createdAt string,
	rootsGeneration int64,
	starred int64,
	coverArtifactID sql.NullString,
	coverRootSessionID sql.NullString,
	coverSource sql.NullString,
	coverUpdatedAt sql.NullString,
	trustEnabledRaw string,
	trustSeenRaw string,
	sessionCount int64,
	lastActivityRaw interface{},
	rootRows []db.ProjectRoots,
) (*Project, error) {
	lastOpened, err := db.ParseTime(lastOpenedAt)
	if err != nil {
		return nil, err
	}
	created, err := db.ParseTime(createdAt)
	if err != nil {
		return nil, err
	}
	lastActivity, err := parseOptionalActivityAt(lastActivityRaw)
	if err != nil {
		return nil, err
	}
	roots, err := rootsFromRows(rootRows)
	if err != nil {
		return nil, err
	}
	isDraft := len(roots) == 1 && roots[0].Kind == RootKindDraft
	trustEnabled, err := decodeTrustEnabled(trustEnabledRaw)
	if err != nil {
		return nil, err
	}
	trustSeen, err := decodeTrustSeen(trustSeenRaw)
	if err != nil {
		return nil, err
	}
	var coverAt *time.Time
	if coverUpdatedAt.Valid && strings.TrimSpace(coverUpdatedAt.String) != "" {
		t, perr := db.ParseTime(coverUpdatedAt.String)
		if perr != nil {
			return nil, perr
		}
		coverAt = &t
	}
	return &Project{
		ID:                 id,
		Name:               db.StringFromNull(name),
		Roots:              roots,
		RootsGeneration:    int(rootsGeneration),
		SessionCount:       int(sessionCount),
		Starred:            starred != 0,
		IsDraft:            isDraft,
		CoverArtifactID:    db.StringFromNull(coverArtifactID),
		CoverRootSessionID: db.StringFromNull(coverRootSessionID),
		CoverSource:        db.StringFromNull(coverSource),
		CoverUpdatedAt:     coverAt,
		TrustEnabled:       trustEnabled,
		TrustSeen:          trustSeen,
		LastActivityAt:     lastActivity,
		LastOpenedAt:       lastOpened,
		CreatedAt:          created,
	}, nil
}

func parseOptionalActivityAt(raw interface{}) (*time.Time, error) {
	if raw == nil {
		return nil, nil
	}
	switch v := raw.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, nil
		}
		t, err := db.ParseTime(v)
		if err != nil {
			return nil, err
		}
		return &t, nil
	case []byte:
		if len(v) == 0 {
			return nil, nil
		}
		t, err := db.ParseTime(string(v))
		if err != nil {
			return nil, err
		}
		return &t, nil
	default:
		return nil, nil
	}
}

func loadProject(ctx context.Context, q *db.Queries, id string) (*Project, error) {
	row, err := q.GetProjectWithStats(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return nil, err
	}
	roots, err := q.ListProjectRoots(ctx, id)
	if err != nil {
		return nil, err
	}
	p, err := projectFromGetStatsRow(row, roots)
	if err != nil {
		return nil, err
	}
	if promotionRow, promotionErr := q.GetProjectPromotion(ctx, id); promotionErr == nil {
		p.Promotion, err = promotionFromRow(promotionRow)
		if err != nil {
			return nil, err
		}
	} else if !errors.Is(promotionErr, sql.ErrNoRows) {
		return nil, promotionErr
	}
	return p, ValidateLifecycle(p)
}

func promotionFromRow(row db.ProjectPromotions) (*Promotion, error) {
	createdAt, err := db.ParseTime(row.CreatedAt)
	if err != nil {
		return nil, err
	}
	updatedAt, err := db.ParseTime(row.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &Promotion{
		ProjectID:       row.ProjectID,
		RootID:          row.RootID,
		SourcePath:      row.SourcePath,
		DestinationPath: row.DestinationPath,
		StagePath:       row.StagePath,
		ReservationPath: row.ReservationPath,
		InitGit:         row.InitGit != 0,
		Phase:           PromotionPhase(row.Phase),
		SourceSHA256:    row.SourceSha256,
		ManifestSHA256:  row.ManifestSha256,
		LastError:       row.LastError,
		CreatedAt:       createdAt,
		UpdatedAt:       updatedAt,
	}, nil
}

func boolToInt64(v bool) int64 {
	if v {
		return 1
	}
	return 0
}
