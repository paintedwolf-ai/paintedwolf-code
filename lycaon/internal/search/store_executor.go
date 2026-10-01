package search

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type queryDatabase interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// StoreExecutor runs parameterized SQL/FTS over evidence_index.
type StoreExecutor struct {
	DB queryDatabase
}

// NewStoreExecutor returns a store-backed search executor.
func NewStoreExecutor(db queryDatabase) *StoreExecutor {
	return &StoreExecutor{DB: db}
}

func (e *StoreExecutor) Source() string { return ExecutorStore }

func (e *StoreExecutor) Run(ctx context.Context, leg PlanLeg) (ExecutorReport, error) {
	if e == nil || e.DB == nil {
		return ExecutorReport{}, fmt.Errorf("store executor database unavailable")
	}
	if leg.Store == nil {
		return ExecutorReport{}, fmt.Errorf("store leg missing")
	}
	var hits []Hit
	var partial bool
	cap := leg.Cap
	if cap <= 0 {
		cap = SearchExecutorProbeHits
	}
	post := leg.Store.Post
	sqlText := leg.Store.SQL
	args := append([]any{}, leg.Store.Args...)
	// A post-filter drops candidates, so it reads well past the result cap —
	// bounding candidates at cap would silently search only the newest rows.
	sqlLimit := cap
	if post != nil {
		scanCap := leg.Store.PostScanCap
		if scanCap <= 0 {
			scanCap = completeStorePostScanRows
		}
		sqlLimit = max(cap+1, scanCap)
	}
	sqlText += " LIMIT ?"
	args = append(args, sqlLimit)
	rows, err := e.DB.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return ExecutorReport{}, err
	}
	defer func() { _ = rows.Close() }()
	candidates := 0
	for rows.Next() {
		candidates++
		row, scanErr := scanStoreRow(rows, post)
		if scanErr != nil {
			return ExecutorReport{}, scanErr
		}
		if post != nil && !post.keep(strings.TrimSpace(row.path.String), row.matchTexts(), row.probeValues()) {
			continue
		}
		hits = append(hits, row.toHit())
		if len(hits) >= cap {
			partial = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return ExecutorReport{}, err
	}
	if candidates >= sqlLimit {
		partial = true
	}
	return ExecutorReport{Hits: hits, Limited: partial}, nil
}

// storeRow is one scanned evidence_index row plus any post-filter probe
// columns.
type storeRow struct {
	id, projectID, hitKind, source                  string
	sessionID, sourceRef, snippet, ts               sql.NullString
	path, url, legID, handle, tool, trust, hintCode sql.NullString
	messageID, messageContent                       sql.NullString
	agentType, kind                                 string
	tombstoned, untrusted                           sql.NullInt64
	verified                                        sql.NullInt64
	line                                            sql.NullInt64
	score                                           sql.NullFloat64
	probes                                          []sql.NullInt64
}

func scanStoreRow(rows *sql.Rows, post *StorePostFilter) (*storeRow, error) {
	row := &storeRow{}
	if post != nil {
		row.probes = make([]sql.NullInt64, post.numProbes)
	}
	dest := []any{
		&row.id, &row.projectID, &row.hitKind, &row.source, &row.sessionID, &row.sourceRef, &row.snippet, &row.ts,
		&row.path, &row.url, &row.legID, &row.handle, &row.tool, &row.trust, &row.verified, &row.hintCode, &row.line,
		&row.agentType, &row.tombstoned, &row.untrusted, &row.kind,
		&row.messageID, &row.messageContent, &row.score,
	}
	for i := range row.probes {
		dest = append(dest, &row.probes[i])
	}
	if err := rows.Scan(dest...); err != nil {
		return nil, err
	}
	return row, nil
}

// matchTexts returns the text sources for full-text candidates.
func (r *storeRow) matchTexts() []string {
	return []string{r.snippet.String, r.path.String, r.url.String, r.messageContent.String}
}

func (r *storeRow) probeValues() []bool {
	out := make([]bool, len(r.probes))
	for i, probe := range r.probes {
		out[i] = probe.Valid && probe.Int64 != 0
	}
	return out
}

func (r *storeRow) toHit() Hit {
	ref := strings.TrimSpace(r.sourceRef.String)
	filePath := strings.TrimSpace(r.path.String)
	snip := strings.TrimSpace(r.snippet.String)
	var verifiedPtr *bool
	if r.verified.Valid {
		v := r.verified.Int64 != 0
		verifiedPtr = &v
	}
	return Hit{
		MessageID: r.messageID.String,
		ID:        stableHitID("store", r.id),
		HitKind:   strings.TrimSpace(r.hitKind),
		Source:    strings.TrimSpace(r.source),
		Score:     r.score.Float64,
		TS:        strings.TrimSpace(r.ts.String),
		Snippet:   snip,
		SourceRef: ref,
		SessionID: strings.TrimSpace(r.sessionID.String),
		ProjectID: strings.TrimSpace(r.projectID),
		LegID:     strings.TrimSpace(r.legID.String),
		Handle:    strings.TrimSpace(r.handle.String),
		Tool:      strings.TrimSpace(r.tool.String),
		Path:      filePath,
		Line:      int(r.line.Int64),
		URL:       strings.TrimSpace(r.url.String),
		Trust:     strings.TrimSpace(r.trust.String),
		Verified:  verifiedPtr,
		HintCode:  strings.TrimSpace(r.hintCode.String),

		AgentType:  strings.TrimSpace(r.agentType),
		Tombstoned: r.tombstoned.Valid && r.tombstoned.Int64 != 0,
		Untrusted:  r.untrusted.Valid && r.untrusted.Int64 != 0,
		Kind:       strings.TrimSpace(r.kind),
	}
}
